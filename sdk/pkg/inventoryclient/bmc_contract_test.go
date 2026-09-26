package inventoryclient_test

import (
	"regexp"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
)

// credentialLike matches field names that could carry BMC credentials or
// secrets. The report messages must never grow such a field (SR-005).
var credentialLike = regexp.MustCompile(`(?i)pass|user|secret|community|cipher|key|auth`)

func TestBmcMessagesCarryNoCredentialField(t *testing.T) {
	for _, md := range []protoreflect.MessageDescriptor{
		(&invv1.Bmc{}).ProtoReflect().Descriptor(),
		(&invv1.BmcPort{}).ProtoReflect().Descriptor(),
	} {
		fields := md.Fields()
		if fields.Len() == 0 {
			t.Fatalf("%s has no fields", md.FullName())
		}
		for i := 0; i < fields.Len(); i++ {
			if name := string(fields.Get(i).Name()); credentialLike.MatchString(name) {
				t.Errorf("%s.%s looks like a credential field", md.FullName(), name)
			}
		}
	}
}

func TestCredentialPatternMatches(t *testing.T) {
	for _, n := range []string{"password", "user_name", "community", "cipher_suite", "auth_type", "kg_key", "secret"} {
		if !credentialLike.MatchString(n) {
			t.Errorf("pattern misses %q", n)
		}
	}
}
