package agentfacts

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Physical disks (feature 023, research D3). Linux disks come from sysfs
// block devices read through an fs.FS rooted at "/" (tests use captured or
// synthetic trees); Windows disks from Get-PhysicalDisk (or Win32_DiskDrive)
// JSON. Virtual block devices (loop, ram, zram, device-mapper, md, nbd,
// optical, floppy, drbd, rbd, eMMC boot partitions) and devices of size 0
// are never reported as physical disks; filesystems are linked to the disks
// they live on, through partitions and device-mapper/md slaves.

// maxAttrBytes bounds every sysfs attribute read.
const maxAttrBytes = 4096

// maxResolveDepth bounds the device-mapper/md slave recursion.
const maxResolveDepth = 8

// excludedBlockPrefixes are virtual or non-disk block devices.
var excludedBlockPrefixes = []string{"loop", "ram", "zram", "dm-", "md", "nbd", "sr", "fd", "drbd", "rbd"}

// TreeSource reads block device facts from FS rooted at "/" (paths
// "sys/block/<dev>/...", "run/udev/data/b<maj:min>"). DevPath resolves the
// /sys/block/<dev> symlink to its /sys/devices path; when nil the resolved
// path is read from the file sys/block/<dev>/devpath (captured trees).
type TreeSource struct {
	FS      fs.FS
	DevPath func(dev string) (string, error)
}

func (s TreeSource) devPath(dev string) string {
	if s.DevPath != nil {
		p, err := s.DevPath(dev)
		if err != nil {
			return ""
		}
		return p
	}
	return s.attr("sys/block/" + dev + "/devpath")
}

// attr reads a sysfs attribute (capped, trimmed); "" when unreadable.
func (s TreeSource) attr(name string) string {
	b, err := readCapped(s.FS, name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (s TreeSource) exists(name string) bool {
	_, err := fs.Stat(s.FS, name)
	return err == nil
}

// readCapped reads at most maxAttrBytes of name.
func readCapped(fsys fs.FS, name string) ([]byte, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxAttrBytes))
}

// BlockDevices is the result of reading the block devices of a host.
type BlockDevices struct {
	Disks        []store.Disk
	Truncated    uint32 // physical disks beyond store.MaxDisks
	Availability string // ok | partial (read errors, cancelled) | unavailable (no sys/block)
}

// ReadBlockDevices reports the physical disks of the tree, sorted by kernel
// name. It stops when ctx ends (partial result).
func ReadBlockDevices(ctx context.Context, src TreeSource) BlockDevices {
	entries, err := fs.ReadDir(src.FS, "sys/block")
	if err != nil {
		return BlockDevices{Availability: store.AvailUnavailable}
	}
	res := BlockDevices{Availability: store.AvailOK}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		if ctx.Err() != nil {
			res.Availability = store.AvailPartial
			break
		}
		if excludedBlock(name) {
			continue
		}
		d, ok, readErr := readDisk(src, name)
		if readErr {
			res.Availability = store.AvailPartial
		}
		if !ok {
			continue
		}
		if len(res.Disks) == store.MaxDisks {
			res.Truncated++
			continue
		}
		res.Disks = append(res.Disks, d)
	}
	return res
}

func excludedBlock(name string) bool {
	for _, p := range excludedBlockPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return strings.HasPrefix(name, "mmcblk") && strings.Contains(name, "boot")
}

// readDisk reads one block device. ok is false for devices that are not
// physical disks (size 0) or unreadable (readErr).
func readDisk(src TreeSource, name string) (d store.Disk, ok, readErr bool) {
	base := "sys/block/" + name + "/"
	sizeRaw, err := readCapped(src.FS, base+"size")
	if err != nil {
		return store.Disk{}, false, true
	}
	sectors, err := strconv.ParseUint(strings.TrimSpace(string(sizeRaw)), 10, 64)
	if err != nil || sectors == 0 || sectors > (1<<63-1)/512 {
		return store.Disk{}, false, false
	}
	iface := interfaceFromPath(src.devPath(name))
	d = store.Disk{
		Name:      clipString(name, store.MaxHWString),
		SizeBytes: sectors * 512,
		Model:     cleanAttr(src.attr(base + "device/model")),
		Vendor:    cleanAttr(src.attr(base + "device/vendor")),
		Interface: iface,
		Serial:    diskSerial(src, name),
		Removable: src.attr(base+"removable") == "1" || iface == store.IfUSB,
	}
	if strings.EqualFold(d.Vendor, "ATA") { // libata's generic SCSI vendor
		d.Vendor = ""
	}
	d.MediaType = mediaType(iface, src.attr(base+"queue/rotational"))
	return d, true, false
}

// diskSerial tries device/serial (NVMe, eMMC), the block serial (virtio),
// VPD page 80h (SCSI/SATA) and the udev database, in that order.
func diskSerial(src TreeSource, name string) string {
	base := "sys/block/" + name + "/"
	for _, p := range []string{base + "device/serial", base + "serial"} {
		if s := cleanAttr(src.attr(p)); s != "" {
			return s
		}
	}
	if b, err := readCapped(src.FS, base+"device/vpd_pg80"); err == nil {
		if s := vpdSerial(b); s != "" {
			return s
		}
	}
	if dev := src.attr(base + "dev"); dev != "" {
		if b, err := readCapped(src.FS, "run/udev/data/b"+dev); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				if v, ok := strings.CutPrefix(line, "E:ID_SERIAL_SHORT="); ok {
					return cleanAttr(v)
				}
			}
		}
	}
	return ""
}

// vpdSerial extracts the unit serial number from SCSI VPD page 80h.
func vpdSerial(b []byte) string {
	if len(b) < 4 || b[1] != 0x80 {
		return ""
	}
	n := int(binary.BigEndian.Uint16(b[2:4]))
	end := min(4+n, len(b))
	return cleanAttr(string(b[4:end]))
}

// cleanAttr trims, drops control characters and invalid UTF-8 and clips.
func cleanAttr(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == 0xfffd {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, ""))
	return clipString(strings.TrimSpace(s), store.MaxHWString)
}

// interfaceFromPath derives the transport from the resolved device path.
func interfaceFromPath(p string) string {
	switch {
	case p == "":
		return store.IfOther
	case strings.Contains(p, "/nvme/"):
		return store.IfNVMe
	case strings.Contains(p, "/usb"):
		return store.IfUSB
	case strings.Contains(p, "/virtio"):
		return store.IfVirtio
	case strings.Contains(p, "VMBUS") || strings.Contains(p, "/vmbus") || strings.Contains(p, "storvsc"):
		return store.IfHyperV
	case strings.Contains(p, "/xen") || strings.Contains(p, "/vbd-"):
		return store.IfXen
	case strings.Contains(p, "/mmc"):
		return store.IfMMC
	case strings.Contains(p, "/end_device-") || strings.Contains(p, "/sas_"):
		return store.IfSAS
	case strings.Contains(p, "/ata"):
		return store.IfSATA
	}
	return store.IfSCSI
}

// mediaType: NVMe namespaces are SSDs; otherwise queue/rotational decides,
// except that virtual and USB transports stay unknown unless they say they
// are not rotational.
func mediaType(iface, rotational string) string {
	switch {
	case iface == store.IfNVMe:
		return store.MediaNVMeSSD
	case rotational == "0":
		return store.MediaSSD
	case rotational == "1" && iface != store.IfVirtio && iface != store.IfHyperV && iface != store.IfXen && iface != store.IfUSB:
		return store.MediaHDD
	}
	return store.MediaUnknown
}

// DiskResolver maps a filesystem's device to the physical disks it lives on.
type DiskResolver interface {
	// Disks returns the physical disk names of device (sorted) and how many
	// were dropped at store.MaxFSDisks.
	Disks(device string) ([]string, uint32)
}

type sysResolver struct {
	src      TreeSource
	physical map[string]bool
	dmNames  map[string]string // device-mapper name -> dm-N
	parents  map[string]string // partition -> disk
}

// NewDiskResolver resolves Linux devices (/dev/sda1, /dev/mapper/vg-root,
// /dev/md0, ...) against the tree: partitions to their disk, device-mapper
// and md devices through their slaves (depth <= 8, cycles stopped) to the
// physical disks named in physical.
func NewDiskResolver(src TreeSource, physical []string) DiskResolver {
	r := &sysResolver{src: src, physical: map[string]bool{}, dmNames: map[string]string{}, parents: map[string]string{}}
	for _, p := range physical {
		r.physical[p] = true
	}
	entries, _ := fs.ReadDir(src.FS, "sys/block")
	for _, e := range entries {
		dev := e.Name()
		if n := src.attr("sys/block/" + dev + "/dm/name"); n != "" {
			r.dmNames[n] = dev
		}
		subs, _ := fs.ReadDir(src.FS, "sys/block/"+dev)
		for _, s := range subs {
			if s.IsDir() && src.exists("sys/block/"+dev+"/"+s.Name()+"/partition") {
				r.parents[s.Name()] = dev
			}
		}
	}
	return r
}

func (r *sysResolver) Disks(device string) ([]string, uint32) {
	name, ok := strings.CutPrefix(device, "/dev/")
	if !ok {
		return nil, 0
	}
	if dm, ok := strings.CutPrefix(name, "mapper/"); ok {
		if name, ok = r.dmNames[dm]; !ok {
			return nil, 0
		}
	}
	found := map[string]bool{}
	r.walk(name, 0, map[string]bool{}, found)
	return boundedSorted(found)
}

func (r *sysResolver) walk(name string, depth int, visited, found map[string]bool) {
	if depth > maxResolveDepth || visited[name] || !fs.ValidPath(name) {
		return
	}
	visited[name] = true
	if parent, ok := r.parents[name]; ok {
		name = parent
	}
	if r.physical[name] {
		found[name] = true
		return
	}
	slaves, _ := fs.ReadDir(r.src.FS, "sys/block/"+name+"/slaves")
	for _, s := range slaves {
		r.walk(s.Name(), depth+1, visited, found)
	}
}

func boundedSorted(found map[string]bool) ([]string, uint32) {
	if len(found) == 0 {
		return nil, 0
	}
	out := make([]string, 0, len(found))
	for n := range found {
		out = append(out, n)
	}
	sort.Strings(out)
	if len(out) > store.MaxFSDisks {
		return out[:store.MaxFSDisks], uint32(len(out) - store.MaxFSDisks) // #nosec G115 -- small
	}
	return out, 0
}

// --- Windows ---

// jsonList decodes a PowerShell ConvertTo-Json result, which is an object
// for a single item and an array for several; null is an empty list.
func jsonList(b []byte, v any) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return errors.New("agentfacts: empty JSON")
	}
	if bytes.Equal(b, []byte("null")) {
		return nil
	}
	if b[0] == '{' {
		b = append(append([]byte{'['}, b...), ']')
	}
	return json.Unmarshal(b, v)
}

// flexString accepts a JSON string or number (PowerShell enum values are
// serialised as numbers by Windows PowerShell 5.1 and as strings elsewhere).
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	if bytes.Equal(b, []byte("null")) {
		*f = ""
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*f = flexString(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("agentfacts: not a string or number: %s", b)
	}
	*f = flexString(n.String())
	return nil
}

// flexSize accepts a size as a JSON number or numeric string; negative or
// unparsable values are 0.
type flexSize uint64

func (f *flexSize) UnmarshalJSON(b []byte) error {
	var s flexString
	if err := s.UnmarshalJSON(b); err != nil {
		return err
	}
	n, err := strconv.ParseUint(string(s), 10, 64)
	if err != nil {
		n = 0
	}
	*f = flexSize(n)
	return nil
}

type winPhysicalDisk struct {
	DeviceID     flexString `json:"DeviceId"`
	FriendlyName flexString `json:"FriendlyName"`
	SerialNumber flexString `json:"SerialNumber"`
	Size         flexSize   `json:"Size"`
	MediaType    flexString `json:"MediaType"`
	BusType      flexString `json:"BusType"`
}

// ParseWindowsPhysicalDisks parses
// `Get-PhysicalDisk | Select-Object DeviceId, FriendlyName, SerialNumber,
// Size, MediaType, BusType | ConvertTo-Json -Compress`.
func ParseWindowsPhysicalDisks(b []byte) ([]store.Disk, error) {
	var in []winPhysicalDisk
	if err := jsonList(b, &in); err != nil {
		return nil, fmt.Errorf("agentfacts: Get-PhysicalDisk JSON: %w", err)
	}
	var out []store.Disk
	for _, w := range in {
		iface, removable := winBusType(string(w.BusType))
		media := winMediaType(string(w.MediaType))
		if iface == store.IfNVMe && (media == store.MediaSSD || media == store.MediaUnknown) {
			media = store.MediaNVMeSSD
		}
		out = append(out, store.Disk{
			Name: cleanAttr("PhysicalDrive" + string(w.DeviceID)), Model: cleanAttr(string(w.FriendlyName)),
			Serial: cleanAttr(string(w.SerialNumber)), SizeBytes: uint64(w.Size), MediaType: media, Interface: iface,
			Removable: removable,
		})
	}
	return out, nil
}

// winBusType maps the MSFT_PhysicalDisk BusType (number or name).
func winBusType(v string) (iface string, removable bool) {
	switch strings.ToLower(v) {
	case "17", "nvme":
		return store.IfNVMe, false
	case "3", "11", "ata", "sata":
		return store.IfSATA, false
	case "10", "sas":
		return store.IfSAS, false
	case "1", "6", "8", "9", "scsi", "fibre channel", "raid", "iscsi":
		return store.IfSCSI, false
	case "7", "usb":
		return store.IfUSB, true
	case "12", "13", "sd", "mmc":
		return store.IfMMC, true
	}
	return store.IfOther, false
}

// winMediaType maps the MSFT_PhysicalDisk MediaType (number or name).
func winMediaType(v string) string {
	switch strings.ToLower(v) {
	case "3", "hdd":
		return store.MediaHDD
	case "4", "ssd":
		return store.MediaSSD
	}
	return store.MediaUnknown
}

type win32DiskDrive struct {
	Index         flexString `json:"Index"`
	Model         flexString `json:"Model"`
	SerialNumber  flexString `json:"SerialNumber"`
	Size          flexSize   `json:"Size"`
	InterfaceType flexString `json:"InterfaceType"`
	MediaType     flexString `json:"MediaType"`
}

// ParseWin32DiskDrives parses the fallback
// `Get-CimInstance Win32_DiskDrive | Select-Object Index, Model,
// SerialNumber, Size, InterfaceType, MediaType | ConvertTo-Json -Compress`
// (systems without the Storage module); the media type is unknown there.
func ParseWin32DiskDrives(b []byte) ([]store.Disk, error) {
	var in []win32DiskDrive
	if err := jsonList(b, &in); err != nil {
		return nil, fmt.Errorf("agentfacts: Win32_DiskDrive JSON: %w", err)
	}
	var out []store.Disk
	for _, w := range in {
		d := store.Disk{Name: cleanAttr("PhysicalDrive" + string(w.Index)), Model: cleanAttr(string(w.Model)),
			Serial: cleanAttr(string(w.SerialNumber)), SizeBytes: uint64(w.Size), MediaType: store.MediaUnknown, Interface: store.IfOther}
		switch strings.ToUpper(string(w.InterfaceType)) {
		case "USB":
			d.Interface, d.Removable = store.IfUSB, true
		case "SCSI":
			d.Interface = store.IfSCSI
		case "IDE":
			d.Interface = store.IfSATA
		}
		out = append(out, d)
	}
	return out, nil
}

type winPartition struct {
	DiskNumber  json.Number `json:"DiskNumber"`
	DriveLetter flexString  `json:"DriveLetter"`
}

// ParseWindowsDriveLetters parses `Get-Partition | Select-Object DiskNumber,
// DriveLetter | ConvertTo-Json -Compress` into "C:" -> "PhysicalDrive0".
// DriveLetter may be a string, a character code or empty/NUL (no letter).
func ParseWindowsDriveLetters(b []byte) (map[string]string, error) {
	var in []winPartition
	if err := jsonList(b, &in); err != nil {
		return nil, fmt.Errorf("agentfacts: Get-Partition JSON: %w", err)
	}
	out := map[string]string{}
	for _, p := range in {
		disk, err := p.DiskNumber.Int64()
		if err != nil || disk < 0 {
			return nil, fmt.Errorf("agentfacts: Get-Partition JSON: bad disk number %q", p.DiskNumber)
		}
		letter := string(p.DriveLetter)
		if n, err := strconv.Atoi(letter); err == nil && len(letter) > 1 && n >= 'A' && n <= 'z' {
			letter = string(rune(n))
		}
		letter = strings.ToUpper(letter)
		if len(letter) != 1 || letter[0] < 'A' || letter[0] > 'Z' {
			continue
		}
		out[letter+":"] = "PhysicalDrive" + strconv.FormatInt(disk, 10)
	}
	return out, nil
}

type driveResolver map[string]string

// WindowsDriveResolver resolves Windows filesystems ("C:", `C:\`) to the
// physical disk holding their drive letter.
func WindowsDriveResolver(letters map[string]string) DiskResolver { return driveResolver(letters) }

func (m driveResolver) Disks(device string) ([]string, uint32) {
	d := strings.ToUpper(strings.TrimRight(device, `\/`))
	if disk, ok := m[path.Clean(d)]; ok {
		return []string{disk}, 0
	}
	return nil, 0
}
