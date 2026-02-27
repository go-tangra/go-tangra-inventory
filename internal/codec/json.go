package codec

import (
	"encoding/json"
	"strconv"

	"github.com/go-kratos/kratos/v2/encoding"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const Name = "json"

func init() {
	encoding.RegisterCodec(jsonCodec{})
}

var (
	marshalOpts = protojson.MarshalOptions{
		EmitUnpopulated: true,
	}
	unmarshalOpts = protojson.UnmarshalOptions{
		DiscardUnknown: true,
	}
)

type jsonCodec struct{}

func (jsonCodec) Marshal(v interface{}) ([]byte, error) {
	msg, ok := v.(proto.Message)
	if !ok {
		return json.Marshal(v)
	}

	data, err := marshalOpts.Marshal(msg)
	if err != nil {
		return nil, err
	}

	// Convert string-encoded 64-bit integers to JSON numbers
	// so the REST API returns numbers instead of strings for uint64/int64 fields.
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return data, nil // fallback to protojson output
	}

	fixInt64Fields(msg.ProtoReflect().Descriptor(), raw)
	return json.Marshal(raw)
}

func (jsonCodec) Unmarshal(data []byte, v interface{}) error {
	msg, ok := v.(proto.Message)
	if !ok {
		return json.Unmarshal(data, v)
	}
	return unmarshalOpts.Unmarshal(data, msg)
}

func (jsonCodec) Name() string { return Name }

// fixInt64Fields walks the JSON map and converts string-encoded 64-bit integers
// to JSON numbers using proto reflection to identify the correct fields.
func fixInt64Fields(desc protoreflect.MessageDescriptor, m map[string]interface{}) {
	for key, val := range m {
		fd := desc.Fields().ByJSONName(key)
		if fd == nil {
			continue
		}

		switch fd.Kind() {
		case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
			if s, ok := val.(string); ok {
				if n, err := strconv.ParseInt(s, 10, 64); err == nil {
					m[key] = n
				}
			}
		case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
			if s, ok := val.(string); ok {
				if n, err := strconv.ParseUint(s, 10, 64); err == nil {
					m[key] = n
				}
			}
		case protoreflect.MessageKind, protoreflect.GroupKind:
			if fd.IsList() {
				if arr, ok := val.([]interface{}); ok {
					for _, item := range arr {
						if sub, ok := item.(map[string]interface{}); ok {
							fixInt64Fields(fd.Message(), sub)
						}
					}
				}
			} else if fd.IsMap() {
				if sub, ok := val.(map[string]interface{}); ok {
					valDesc := fd.MapValue()
					if valDesc.Kind() == protoreflect.MessageKind {
						for _, v := range sub {
							if entry, ok := v.(map[string]interface{}); ok {
								fixInt64Fields(valDesc.Message(), entry)
							}
						}
					}
				}
			} else {
				if sub, ok := val.(map[string]interface{}); ok {
					fixInt64Fields(fd.Message(), sub)
				}
			}
		}
	}
}
