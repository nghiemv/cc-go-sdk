package cc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// initFSBPluginManagerForTest spins up an FSB-backed PluginManager with an
// empty payload so variable-substitution unit tests no longer require S3
// credentials to boot. Before FSB was revived, these tests had to call
// InitPluginManager() which always routed to S3 and failed locally.
func initFSBPluginManagerForTest(t *testing.T) *PluginManager {
	t.Helper()
	root := t.TempDir()
	t.Setenv("CC_STORE_TYPE", "FS")
	t.Setenv("FSB_ROOT_PATH", root)
	t.Setenv("CC_MANIFEST_ID", "unit-manifest")
	t.Setenv("CC_PAYLOAD_ID", "unit-payload")
	t.Setenv("CC_EVENT_IDENTIFIER", "unit-event")
	t.Setenv("TESTV3", "98765432")
	t.Setenv("V5TEST1", "this is a test")

	payloadDir := filepath.Join(root, "unit-payload")
	if err := os.MkdirAll(payloadDir, 0o755); err != nil {
		t.Fatalf("mkdir payload dir: %v", err)
	}
	empty := Payload{
		IOManager: IOManager{
			Attributes: PayloadAttributes{},
			Stores:     []DataStore{},
			Inputs:     []DataSource{},
			Outputs:    []DataSource{},
		},
		Actions: []Action{},
	}
	bytes, err := json.Marshal(empty)
	if err != nil {
		t.Fatalf("marshal empty payload: %v", err)
	}
	if err := os.WriteFile(filepath.Join(payloadDir, payloadFileName), bytes, 0o644); err != nil {
		t.Fatalf("write empty payload: %v", err)
	}

	pm, err := InitPluginManager()
	if err != nil {
		t.Fatalf("InitPluginManager failed: %v", err)
	}
	return pm
}

func TestSubstituteMapVariablesEnvOnly(t *testing.T) {
	pm := initFSBPluginManagerForTest(t)

	payloadAttrs := map[string]any{
		"val1":   1,
		"val2":   "two",
		"val3":   "this is a {ENV::TESTV3}",
		"val4":   "this is {ATTR::testv4} ok?",
		"testv4": "LOREM IPSUM",
		"val5": map[string]any{
			"v5test1": "test 1 of val5",
			"v5test2": "this is a test of {ENV::V5TEST1}",
			"v5test3": []string{
				"v5t3-1{ENV::TESTV3}-ok",
				"v5t3-2{ENV::TESTV3}-ok",
				"v5t3-3{ENV::TESTV3}-ok",
				"v5t3-4{ENV::TESTV3}-ok",
			},
		},
	}
	pm.Attributes = payloadAttrs

	pm.substituteMapVariables(payloadAttrs, false)

	// Note: handleSliceSub converts a []string field into []any after
	// substitution — it builds a fresh []any{} and copies values in.
	// The expected map therefore uses []any, not []string, for v5test3.
	expectedResult := map[string]any{
		"val1":   1,
		"val2":   "two",
		"val3":   "this is a 98765432",
		"val4":   "this is {ATTR::testv4} ok?",
		"testv4": "LOREM IPSUM",
		"val5": map[string]any{
			"v5test1": "test 1 of val5",
			"v5test2": "this is a test of this is a test",
			"v5test3": []any{
				"v5t3-198765432-ok",
				"v5t3-298765432-ok",
				"v5t3-398765432-ok",
				"v5t3-498765432-ok",
			},
		},
	}

	if !reflect.DeepEqual(map[string]any(pm.Attributes), expectedResult) {
		t.Fatalf("expected: %v found %v", expectedResult, pm.Attributes)
	}
}

func TestSubstituteMapVariables(t *testing.T) {
	pm := initFSBPluginManagerForTest(t)

	payloadAttrs := map[string]any{
		"val1":   1,
		"val2":   "two",
		"val3":   "this is a {ENV::TESTV3}",
		"val4":   "this is {ATTR::testv4} ok?",
		"testv4": "LOREM IPSUM",
		"val5": map[string]any{
			"v5test1": "test 1 of val5",
			"v5test2": "this is a test of {ENV::V5TEST1}",
			"v5test3": []string{
				"v5t3-1{ENV::TESTV3}-ok",
				"v5t3-2{ENV::TESTV3}-ok",
				"v5t3-3{ENV::TESTV3}-ok",
				"v5t3-4{ENV::TESTV3}-ok",
			},
		},
	}
	pm.Attributes = payloadAttrs
	pm.Actions = []Action{
		{
			IOManager: IOManager{
				Attributes: payloadAttrs,
			},
		},
	}

	pm.substituteMapVariables(pm.Actions[0].Attributes, true)

	// See comment in TestSubstituteMapVariablesEnvOnly: v5test3 transitions
	// from []string to []any when passed through handleSliceSub.
	expectedResult := map[string]any{
		"val1":   1,
		"val2":   "two",
		"val3":   "this is a 98765432",
		"val4":   "this is LOREM IPSUM ok?",
		"testv4": "LOREM IPSUM",
		"val5": map[string]any{
			"v5test1": "test 1 of val5",
			"v5test2": "this is a test of this is a test",
			"v5test3": []any{
				"v5t3-198765432-ok",
				"v5t3-298765432-ok",
				"v5t3-398765432-ok",
				"v5t3-498765432-ok",
			},
		},
	}

	if !reflect.DeepEqual(map[string]any(pm.Attributes), expectedResult) {
		t.Fatalf("expected: %v found %v", expectedResult, pm.Attributes)
	}
}
