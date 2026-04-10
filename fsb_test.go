package cc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestConfig represents test configuration loaded from JSON
type TestConfig struct {
	FSBTest struct {
		StoreType  string `json:"store_type"`
		RootPath   string `json:"root_path"`
		ManifestID string `json:"manifest_id"`
		PayloadID  string `json:"payload_id"`
	} `json:"fsb_test"`
	IntegrationTest struct {
		StoreType  string `json:"store_type"`
		RootPath   string `json:"root_path"`
		ManifestID string `json:"manifest_id"`
		PayloadID  string `json:"payload_id"`
	} `json:"integration_test"`
}

// loadTestConfig loads test configuration from JSON file.
func loadTestConfig(t *testing.T) TestConfig {
	t.Helper()
	data, err := os.ReadFile("testdata/test_config.json")
	if err != nil {
		t.Fatalf("Failed to read test config: %v", err)
	}
	var config TestConfig
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("Failed to unmarshal test config: %v", err)
	}
	return config
}

// setupFSBTestEnv sets the canonical FSB env vars via t.Setenv so they are
// automatically unset at test end. Using t.Setenv is critical — it prevents
// FSB env leaking into the existing TestSubstituteMapVariables* tests that
// call InitPluginManager with no explicit setup.
func setupFSBTestEnv(t *testing.T, rootPath, manifestID, payloadID string) {
	t.Helper()
	t.Setenv("CC_STORE_TYPE", "FS")
	t.Setenv("FSB_ROOT_PATH", rootPath)
	t.Setenv("CC_MANIFEST_ID", manifestID)
	t.Setenv("CC_PAYLOAD_ID", payloadID)
	t.Setenv("CC_EVENT_IDENTIFIER", "test-event")
}

// freshFSBRoot returns a unique temporary FSB root for a test and arranges
// for cleanup. Uses t.TempDir so nothing is left behind even on failure.
func freshFSBRoot(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// TestFSBCcStoreDispatch verifies NewCcStore routes the "FS" store type to
// the FSB backend and that the backend reports itself as handling FSB. This
// is the regression test for commit 7076c19, which accidentally disabled the
// FSB dispatch branch.
func TestFSBCcStoreDispatch(t *testing.T) {
	root := freshFSBRoot(t)
	setupFSBTestEnv(t, root, "test-manifest", "test-payload")

	store, err := NewCcStore(nil)
	if err != nil {
		t.Fatalf("NewCcStore returned error: %v", err)
	}
	if !store.HandlesDataStoreType(FSB) {
		t.Error("FSB store should report that it handles FSB data store type")
	}
	if store.HandlesDataStoreType(FSS3) {
		t.Error("FSB store should not report that it handles S3 data store type")
	}
	if store.RootPath() != localRootPath {
		t.Errorf("unexpected local root path: got %s want %s", store.RootPath(), localRootPath)
	}
}

// TestFSBCcStorePayloadRoundTrip exercises SetPayload -> GetPayload through
// NewCcStore using the FSB backend. Also verifies that PutObject/GetObject
// on a Memory-state object preserve bytes exactly.
func TestFSBCcStorePayloadRoundTrip(t *testing.T) {
	root := freshFSBRoot(t)
	manifestID := "round-trip-manifest"
	payloadID := "round-trip-payload"
	setupFSBTestEnv(t, root, manifestID, payloadID)

	store, err := NewCcStore(nil)
	if err != nil {
		t.Fatalf("NewCcStore returned error: %v", err)
	}

	p := Payload{
		IOManager: IOManager{
			Attributes: PayloadAttributes{"hello": "world"},
			Stores:     []DataStore{},
			Inputs:     []DataSource{},
			Outputs:    []DataSource{},
		},
		Actions: []Action{},
	}
	if err := store.SetPayload(p); err != nil {
		t.Fatalf("SetPayload failed: %v", err)
	}

	got, err := store.GetPayload()
	if err != nil {
		t.Fatalf("GetPayload failed: %v", err)
	}
	if v, ok := got.Attributes["hello"].(string); !ok || v != "world" {
		t.Errorf("payload attribute round-trip mismatch: got %+v", got.Attributes)
	}

	// PutObject (Memory) then GetObject
	data := []byte("fsb object payload bytes")
	if err := store.PutObject(PutObjectInput{
		FileName:      "sample",
		FileExtension: "txt",
		ObjectState:   Memory,
		Data:          data,
	}); err != nil {
		t.Fatalf("PutObject failed: %v", err)
	}

	// GetObject reads from remoteRootPath/manifestId/<file>
	got2, err := store.GetObject(GetObjectInput{
		SourceRootPath: root,
		FileName:       "sample",
		FileExtension:  "txt",
	})
	if err != nil {
		t.Fatalf("GetObject failed: %v", err)
	}
	if string(got2) != string(data) {
		t.Errorf("GetObject data mismatch: got %q want %q", string(got2), string(data))
	}

	// Physical location sanity check: the file should live under root/manifest
	expected := filepath.Join(root, manifestID, "sample.txt")
	if _, err := os.Stat(expected); err != nil {
		t.Errorf("expected file %s to exist, stat err: %v", expected, err)
	}
}

// TestFSBPluginManagerEndToEnd is the end-to-end round-trip that was
// missing when FSB was introduced. It writes a realistic payload (with
// per-payload FSB DataStore + Input/Output DataSources) to disk, initializes
// a PluginManager, and drives CopyFileToLocal + CopyFileToRemote through the
// IOManager. This is the test that would have caught the March 2026
// regression because it exercises the full NewCcStore -> GetPayload ->
// connectStores -> FileDataStore.Connect -> filesapi.BlockFS chain.
func TestFSBPluginManagerEndToEnd(t *testing.T) {
	root := freshFSBRoot(t)
	manifestID := "e2e-manifest"
	payloadID := "e2e-payload"
	setupFSBTestEnv(t, root, manifestID, payloadID)

	// Stage 1: prepare the "remote" store layout.
	//   - A per-payload DataStore rooted at <root>/remote.
	//   - An "inputs" directory with a pre-existing file we'll pull to local.
	//   - A local scratch directory where we'll later stage a file to push
	//     back to the "remote" store.
	remoteRoot := filepath.Join(root, "remote")
	inputsDir := filepath.Join(remoteRoot, "inputs")
	if err := os.MkdirAll(inputsDir, 0o755); err != nil {
		t.Fatalf("mkdir inputs: %v", err)
	}
	inputFileContents := []byte("hello from fsb input")
	if err := os.WriteFile(filepath.Join(inputsDir, "seed.txt"), inputFileContents, 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	// Stage 2: build a Payload that references the remote store via FSB and
	// declare one input (the seed file) and one output (where we'll push).
	payload := Payload{
		IOManager: IOManager{
			Attributes: PayloadAttributes{},
			Stores: []DataStore{
				{
					Name:      "local-fsb",
					StoreType: FSB,
					Parameters: PayloadAttributes{
						"root": remoteRoot,
					},
				},
			},
			Inputs: []DataSource{
				{
					Name:      "seed-input",
					StoreName: "local-fsb",
					Paths: map[string]string{
						"default": "inputs/seed.txt",
					},
				},
			},
			Outputs: []DataSource{
				{
					Name:      "result-output",
					StoreName: "local-fsb",
					Paths: map[string]string{
						"default": "outputs/result.txt",
					},
				},
			},
		},
		Actions: []Action{},
	}

	// Stage 3: write the payload file where the FSB CcStore will look for it.
	// FSBCcStore.GetPayload reads <FSB_ROOT_PATH>/<payloadId>/payload.
	payloadDir := filepath.Join(root, payloadID)
	if err := os.MkdirAll(payloadDir, 0o755); err != nil {
		t.Fatalf("mkdir payload dir: %v", err)
	}
	pbytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if err := os.WriteFile(filepath.Join(payloadDir, payloadFileName), pbytes, 0o644); err != nil {
		t.Fatalf("write payload file: %v", err)
	}

	// Stage 4: init the plugin manager. This exercises NewCcStore -> FSB,
	// GetPayload -> JSON decode, connectStores -> FileDataStore.Connect(FSB).
	pm, err := InitPluginManager()
	if err != nil {
		t.Fatalf("InitPluginManager failed: %v", err)
	}
	if pm.ccStore == nil {
		t.Fatal("PluginManager has no ccStore")
	}
	if len(pm.Stores) != 1 {
		t.Fatalf("expected 1 store on payload, got %d", len(pm.Stores))
	}
	if pm.Stores[0].Session == nil {
		t.Fatal("FSB DataStore Session is nil — connectStores did not populate the BlockFS filestore (regression check)")
	}
	if _, ok := pm.Stores[0].Session.(FileDataStoreInterface); !ok {
		t.Fatalf("FSB DataStore Session does not implement FileDataStoreInterface, got %T", pm.Stores[0].Session)
	}

	// Stage 5: round-trip via IOManager.
	//
	// 5a: CopyFileToLocal pulls the seed file into a fresh local scratch dir.
	localDir := filepath.Join(root, "local-scratch")
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		t.Fatalf("mkdir local dir: %v", err)
	}
	if err := pm.CopyFileToLocal(CopyToLocalInput{
		DsName:    "seed-input",
		PathKey:   "default",
		LocalPath: localDir,
	}); err != nil {
		t.Fatalf("CopyFileToLocal failed: %v", err)
	}
	localCopy := filepath.Join(localDir, "seed.txt")
	got, err := os.ReadFile(localCopy)
	if err != nil {
		t.Fatalf("read local copy: %v", err)
	}
	if string(got) != string(inputFileContents) {
		t.Errorf("local copy content mismatch: got %q want %q", string(got), string(inputFileContents))
	}

	// 5b: CopyFileToRemote pushes a new file from local into the remote
	// outputs location via the "result-output" DataSource.
	localSrc := filepath.Join(localDir, "produced.txt")
	producedContents := []byte("hello from fsb output")
	if err := os.WriteFile(localSrc, producedContents, 0o644); err != nil {
		t.Fatalf("write produced: %v", err)
	}
	if err := pm.CopyFileToRemote(CopyFileToRemoteInput{
		RemoteDsName: "result-output",
		DsPathKey:    "default",
		LocalPath:    localSrc,
	}); err != nil {
		t.Fatalf("CopyFileToRemote failed: %v", err)
	}

	// Verify the file landed at <remoteRoot>/outputs/result.txt.
	remoteLanding := filepath.Join(remoteRoot, "outputs", "result.txt")
	landed, err := os.ReadFile(remoteLanding)
	if err != nil {
		t.Fatalf("read remote landing: %v", err)
	}
	if string(landed) != string(producedContents) {
		t.Errorf("remote landing content mismatch: got %q want %q", string(landed), string(producedContents))
	}
}

// TestNewCcStoreUnknownType ensures unknown store types still error out,
// covering the default arm of the NewCcStore switch.
func TestNewCcStoreUnknownType(t *testing.T) {
	t.Setenv("CC_STORE_TYPE", "NOT_A_REAL_STORE")
	_, err := NewCcStore(nil)
	if err == nil {
		t.Error("expected error for unknown store type, got nil")
	}
}
