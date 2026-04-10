package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	cc "github.com/usace-cloud-compute/cc-go-sdk"
)

// ---------------------------------------------------------------------------
// JSON protocol types (serve mode)
// ---------------------------------------------------------------------------

type request struct {
	ID         string `json:"id"`
	Cmd        string `json:"cmd"`
	DsName     string `json:"ds_name,omitempty"`
	PathKey    string `json:"pathkey,omitempty"`
	DataKey    string `json:"datakey,omitempty"`
	LocalPath  string `json:"localpath,omitempty"`
	DataB64    string `json:"data_b64,omitempty"`
	SrcDs      string `json:"src_ds,omitempty"`
	SrcPathKey string `json:"src_pathkey,omitempty"`
	SrcDataKey string `json:"src_datakey,omitempty"`
	DstDs      string `json:"dst_ds,omitempty"`
	DstPathKey string `json:"dst_pathkey,omitempty"`
	DstDataKey string `json:"dst_datakey,omitempty"`
}

type response struct {
	ID      string `json:"id,omitempty"`
	OK      bool   `json:"ok"`
	Cmd     string `json:"cmd,omitempty"`
	Data    any    `json:"data,omitempty"`
	DataB64 string `json:"data_b64,omitempty"`
	Error   string `json:"error,omitempty"`
}

// ---------------------------------------------------------------------------
// Payload mirror structs (snake_case JSON)
// ---------------------------------------------------------------------------

type payloadJSON struct {
	Attributes map[string]any   `json:"attributes"`
	Stores     []storeJSON      `json:"stores"`
	Inputs     []dataSourceJSON `json:"inputs"`
	Outputs    []dataSourceJSON `json:"outputs"`
	Actions    []actionJSON     `json:"actions"`
}

type storeJSON struct {
	Name      string         `json:"name"`
	StoreType string         `json:"store_type"`
	Profile   string         `json:"profile"`
	Params    map[string]any `json:"params"`
	ID        string         `json:"id"`
}

type dataSourceJSON struct {
	Name      string            `json:"name"`
	Paths     map[string]string `json:"paths"`
	StoreName string            `json:"store_name"`
	DataPaths map[string]string `json:"data_paths"`
	ID        string            `json:"id"`
}

type actionJSON struct {
	Name        string           `json:"name"`
	Type        string           `json:"type"`
	Description string           `json:"description"`
	Attributes  map[string]any   `json:"attributes"`
	Stores      []storeJSON      `json:"stores"`
	Inputs      []dataSourceJSON `json:"inputs"`
	Outputs     []dataSourceJSON `json:"outputs"`
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func usage() {
	fmt.Fprintln(os.Stderr, `Usage: cc-sdk <command> [args...]

Commands:
  serve                                                Long-running JSON-line mode over stdin/stdout
  get-payload                                          Read payload JSON to stdout
  copy-to-local  <ds> <pathkey> [datakey] <localpath>  Download remote object to local file
  copy-to-remote <ds> <pathkey> [datakey] <localpath>  Upload local file to remote store
  copy-folder-to-remote <ds> <pathkey> [datakey] <localpath>  Recursively upload local directory
  get            <ds> <pathkey> [datakey]               Fetch object bytes to stdout
  put            <ds> <pathkey> [datakey]               Read stdin bytes, upload to destination
  copy           <src_ds> <src_pathkey> [src_datakey] <dst_ds> <dst_pathkey> [dst_datakey]

Environment variables (read by cc-go-sdk):
  CC_PAYLOAD_ID, CC_MANIFEST_ID, CC_EVENT_IDENTIFIER, CC_STORE_TYPE,
  CC_AWS_ACCESS_KEY_ID, CC_AWS_SECRET_ACCESS_KEY, CC_AWS_DEFAULT_REGION, CC_AWS_S3_BUCKET`)
}

func fatal(msg string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+msg+"\n", args...)
	os.Exit(1)
}

func initPM() *cc.PluginManager {
	pm, err := cc.InitPluginManager()
	if err != nil {
		fatal("initializing plugin manager: %v", err)
	}
	return pm
}

func convertStores(stores []cc.DataStore) []storeJSON {
	out := make([]storeJSON, 0, len(stores))
	for _, s := range stores {
		id := ""
		if s.ID != nil {
			id = s.ID.String()
		}
		params := map[string]any(s.Parameters)
		if params == nil {
			params = map[string]any{}
		}
		out = append(out, storeJSON{
			Name:      s.Name,
			StoreType: string(s.StoreType),
			Profile:   s.DsProfile,
			Params:    params,
			ID:        id,
		})
	}
	return out
}

func convertDataSources(sources []cc.DataSource) []dataSourceJSON {
	out := make([]dataSourceJSON, 0, len(sources))
	for _, ds := range sources {
		id := ""
		if ds.ID != nil {
			id = ds.ID.String()
		}
		paths := ds.Paths
		if paths == nil {
			paths = map[string]string{}
		}
		dataPaths := ds.DataPaths
		if dataPaths == nil {
			dataPaths = map[string]string{}
		}
		out = append(out, dataSourceJSON{
			Name:      ds.Name,
			Paths:     paths,
			StoreName: ds.StoreName,
			DataPaths: dataPaths,
			ID:        id,
		})
	}
	return out
}

func buildPayloadJSON(pm *cc.PluginManager) payloadJSON {
	attrs := map[string]any(pm.Attributes)
	if attrs == nil {
		attrs = map[string]any{}
	}

	actions := make([]actionJSON, 0, len(pm.Actions))
	for _, a := range pm.Actions {
		aAttrs := map[string]any(a.Attributes)
		if aAttrs == nil {
			aAttrs = map[string]any{}
		}
		actions = append(actions, actionJSON{
			Name:        a.Name,
			Type:        a.Type,
			Description: a.Description,
			Attributes:  aAttrs,
			Stores:      convertStores(a.Stores),
			Inputs:      convertDataSources(a.Inputs),
			Outputs:     convertDataSources(a.Outputs),
		})
	}

	return payloadJSON{
		Attributes: attrs,
		Stores:     convertStores(pm.Stores),
		Inputs:     convertDataSources(pm.Inputs),
		Outputs:    convertDataSources(pm.Outputs),
		Actions:    actions,
	}
}

// ---------------------------------------------------------------------------
// Shared command handlers (used by both one-shot and serve mode)
//
// Each handler returns (response-data, error). The caller is responsible for
// writing the result to stdout (serve) or handling the error (one-shot).
// ---------------------------------------------------------------------------

func handleGetPayload(pm *cc.PluginManager) (payloadJSON, error) {
	return buildPayloadJSON(pm), nil
}

func handleCopyToLocal(pm *cc.PluginManager, dsName, pathKey, dataKey, localPath string) error {
	return pm.CopyFileToLocal(cc.CopyToLocalInput{
		DsName:    dsName,
		PathKey:   pathKey,
		LocalPath: localPath,
	})
}

func handleCopyToRemote(pm *cc.PluginManager, dsName, pathKey, dataKey, localPath string) error {
	input := cc.CopyFileToRemoteInput{
		RemoteDsName:  dsName,
		DsPathKey:     pathKey,
		DsDataPathKey: dataKey,
		LocalPath:     localPath,
	}
	return pm.CopyFileToRemote(input)
}

func handleCopyFolderToRemote(pm *cc.PluginManager, dsName, pathKey, dataKey, localPath string) error {
	return filepath.Walk(localPath, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}

		relPath, err := filepath.Rel(localPath, path)
		if err != nil {
			return fmt.Errorf("computing relative path: %w", err)
		}

		ds, err := pm.GetOutputDataSource(dsName)
		if err != nil {
			return fmt.Errorf("getting output data source %s: %w", dsName, err)
		}

		basePath, ok := ds.Paths[pathKey]
		if !ok {
			return fmt.Errorf("path key %s not found in data source %s", pathKey, dsName)
		}

		remotePath := basePath + "/" + filepath.ToSlash(relPath)

		store, err := pm.GetStore(ds.StoreName)
		if err != nil {
			return fmt.Errorf("getting store %s: %w", ds.StoreName, err)
		}

		writer, ok := store.Session.(cc.StoreWriter)
		if !ok {
			return fmt.Errorf("store %s does not implement StoreWriter", ds.StoreName)
		}

		f, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("opening local file %s: %w", path, err)
		}
		defer f.Close()

		datapath := ""
		if dataKey != "" {
			if dp, ok := ds.DataPaths[dataKey]; ok {
				datapath = dp
			}
		}

		_, err = writer.Put(f, remotePath, datapath)
		if err != nil {
			return fmt.Errorf("uploading %s: %w", relPath, err)
		}

		fmt.Fprintf(os.Stderr, "uploaded: %s\n", relPath)
		return nil
	})
}

func handleGet(pm *cc.PluginManager, dsName, pathKey, dataKey string) ([]byte, error) {
	input := cc.DataSourceOpInput{
		DataSourceName: dsName,
		PathKey:        pathKey,
		DataPathKey:    dataKey,
	}

	reader, err := pm.GetReader(input)
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, reader); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func handlePut(pm *cc.PluginManager, dsName, pathKey, dataKey string, src io.Reader) error {
	input := cc.PutOpInput{
		SrcReader: src,
		DataSourceOpInput: cc.DataSourceOpInput{
			DataSourceName: dsName,
			PathKey:        pathKey,
			DataPathKey:    dataKey,
		},
	}

	_, err := pm.Put(input)
	return err
}

func handleCopy(pm *cc.PluginManager, srcDs, srcPathKey, srcDataKey, dstDs, dstPathKey, dstDataKey string) error {
	src := cc.DataSourceOpInput{
		DataSourceName: srcDs,
		PathKey:        srcPathKey,
		DataPathKey:    srcDataKey,
	}
	dst := cc.DataSourceOpInput{
		DataSourceName: dstDs,
		PathKey:        dstPathKey,
		DataPathKey:    dstDataKey,
	}
	return pm.Copy(src, dst)
}

// ---------------------------------------------------------------------------
// main dispatch
// ---------------------------------------------------------------------------

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "serve":
		cmdServe()
	case "get-payload":
		cmdGetPayload()
	case "copy-to-local":
		cmdCopyToLocal()
	case "copy-to-remote":
		cmdCopyToRemote()
	case "copy-folder-to-remote":
		cmdCopyFolderToRemote()
	case "get":
		cmdGet()
	case "put":
		cmdPut()
	case "copy":
		cmdCopy()
	default:
		usage()
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// serve mode
// ---------------------------------------------------------------------------

func cmdServe() {
	pm, err := cc.InitPluginManager()
	if err != nil {
		// In serve mode, log errors to stderr
		fmt.Fprintf(os.Stderr, "error: initializing plugin manager: %v\n", err)
		os.Exit(1)
	}

	enc := json.NewEncoder(os.Stdout)

	// Send the ready signal
	if err := enc.Encode(response{OK: true, Cmd: "ready"}); err != nil {
		fmt.Fprintf(os.Stderr, "error: writing ready signal: %v\n", err)
		os.Exit(1)
	}

	scanner := bufio.NewScanner(os.Stdin)
	// Allow up to 10 MB per line (base64 payloads can be large)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			// Malformed JSON: respond with error, don't crash
			_ = enc.Encode(response{OK: false, Error: fmt.Sprintf("malformed request: %v", err)})
			continue
		}

		resp := dispatchServeRequest(pm, &req)
		if err := enc.Encode(resp); err != nil {
			fmt.Fprintf(os.Stderr, "error: writing response for id=%s: %v\n", req.ID, err)
		}

		// shutdown command: exit cleanly after responding
		if req.Cmd == "shutdown" {
			return
		}
	}

	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "error: reading stdin: %v\n", err)
		os.Exit(1)
	}
}

func dispatchServeRequest(pm *cc.PluginManager, req *request) response {
	switch req.Cmd {
	case "get-payload":
		p, _ := handleGetPayload(pm)
		return response{ID: req.ID, OK: true, Data: p}

	case "copy-to-local":
		if err := handleCopyToLocal(pm, req.DsName, req.PathKey, req.DataKey, req.LocalPath); err != nil {
			return response{ID: req.ID, OK: false, Error: err.Error()}
		}
		return response{ID: req.ID, OK: true}

	case "copy-to-remote":
		if err := handleCopyToRemote(pm, req.DsName, req.PathKey, req.DataKey, req.LocalPath); err != nil {
			return response{ID: req.ID, OK: false, Error: err.Error()}
		}
		return response{ID: req.ID, OK: true}

	case "copy-folder-to-remote":
		if err := handleCopyFolderToRemote(pm, req.DsName, req.PathKey, req.DataKey, req.LocalPath); err != nil {
			return response{ID: req.ID, OK: false, Error: err.Error()}
		}
		return response{ID: req.ID, OK: true}

	case "get":
		data, err := handleGet(pm, req.DsName, req.PathKey, req.DataKey)
		if err != nil {
			return response{ID: req.ID, OK: false, Error: err.Error()}
		}
		return response{ID: req.ID, OK: true, DataB64: base64.StdEncoding.EncodeToString(data)}

	case "put":
		raw, err := base64.StdEncoding.DecodeString(req.DataB64)
		if err != nil {
			return response{ID: req.ID, OK: false, Error: fmt.Sprintf("invalid base64 data: %v", err)}
		}
		if err := handlePut(pm, req.DsName, req.PathKey, req.DataKey, bytes.NewReader(raw)); err != nil {
			return response{ID: req.ID, OK: false, Error: err.Error()}
		}
		return response{ID: req.ID, OK: true}

	case "copy":
		if err := handleCopy(pm, req.SrcDs, req.SrcPathKey, req.SrcDataKey, req.DstDs, req.DstPathKey, req.DstDataKey); err != nil {
			return response{ID: req.ID, OK: false, Error: err.Error()}
		}
		return response{ID: req.ID, OK: true}

	case "shutdown":
		return response{ID: req.ID, OK: true}

	default:
		return response{ID: req.ID, OK: false, Error: fmt.Sprintf("unknown command: %s", req.Cmd)}
	}
}

// ---------------------------------------------------------------------------
// One-shot CLI commands
// ---------------------------------------------------------------------------

func cmdGetPayload() {
	pm := initPM()
	p := buildPayloadJSON(pm)

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(p); err != nil {
		fatal("encoding payload: %v", err)
	}
}

func cmdCopyToLocal() {
	args := os.Args[2:]
	var dsName, pathKey, dataKey, localPath string

	switch len(args) {
	case 3:
		dsName, pathKey, localPath = args[0], args[1], args[2]
	case 4:
		dsName, pathKey, dataKey, localPath = args[0], args[1], args[2], args[3]
	default:
		fatal("copy-to-local requires 3 or 4 arguments: <ds_name> <pathkey> [datakey] <localpath>")
	}

	pm := initPM()
	if err := handleCopyToLocal(pm, dsName, pathKey, dataKey, localPath); err != nil {
		fatal("copy-to-local: %v", err)
	}
}

func cmdCopyToRemote() {
	args := os.Args[2:]
	var dsName, pathKey, dataKey, localPath string

	switch len(args) {
	case 3:
		dsName, pathKey, localPath = args[0], args[1], args[2]
	case 4:
		dsName, pathKey, dataKey, localPath = args[0], args[1], args[2], args[3]
	default:
		fatal("copy-to-remote requires 3 or 4 arguments: <ds_name> <pathkey> [datakey] <localpath>")
	}

	pm := initPM()
	if err := handleCopyToRemote(pm, dsName, pathKey, dataKey, localPath); err != nil {
		fatal("copy-to-remote: %v", err)
	}
}

func cmdCopyFolderToRemote() {
	args := os.Args[2:]
	var dsName, pathKey, dataKey, localPath string

	switch len(args) {
	case 3:
		dsName, pathKey, localPath = args[0], args[1], args[2]
	case 4:
		dsName, pathKey, dataKey, localPath = args[0], args[1], args[2], args[3]
	default:
		fatal("copy-folder-to-remote requires 3 or 4 arguments: <ds_name> <pathkey> [datakey] <localpath>")
	}

	pm := initPM()
	if err := handleCopyFolderToRemote(pm, dsName, pathKey, dataKey, localPath); err != nil {
		fatal("copy-folder-to-remote: %v", err)
	}
}

func cmdGet() {
	args := os.Args[2:]
	var dsName, pathKey, dataKey string

	switch len(args) {
	case 2:
		dsName, pathKey = args[0], args[1]
	case 3:
		dsName, pathKey, dataKey = args[0], args[1], args[2]
	default:
		fatal("get requires 2 or 3 arguments: <ds_name> <pathkey> [datakey]")
	}

	pm := initPM()
	data, err := handleGet(pm, dsName, pathKey, dataKey)
	if err != nil {
		fatal("get: %v", err)
	}

	if _, err := os.Stdout.Write(data); err != nil {
		fatal("get: writing to stdout: %v", err)
	}
}

func cmdPut() {
	args := os.Args[2:]
	var dsName, pathKey, dataKey string

	switch len(args) {
	case 2:
		dsName, pathKey = args[0], args[1]
	case 3:
		dsName, pathKey, dataKey = args[0], args[1], args[2]
	default:
		fatal("put requires 2 or 3 arguments: <ds_name> <pathkey> [datakey]")
	}

	pm := initPM()
	if err := handlePut(pm, dsName, pathKey, dataKey, os.Stdin); err != nil {
		fatal("put: %v", err)
	}
}

func cmdCopy() {
	args := os.Args[2:]
	var srcDs, srcPathKey, srcDataKey, dstDs, dstPathKey, dstDataKey string

	switch len(args) {
	case 4:
		srcDs, srcPathKey = args[0], args[1]
		dstDs, dstPathKey = args[2], args[3]
	case 6:
		srcDs, srcPathKey, srcDataKey = args[0], args[1], args[2]
		dstDs, dstPathKey, dstDataKey = args[3], args[4], args[5]
	default:
		fatal("copy requires 4 or 6 arguments: <src_ds> <src_pathkey> [src_datakey] <dst_ds> <dst_pathkey> [dst_datakey]")
	}

	pm := initPM()
	if err := handleCopy(pm, srcDs, srcPathKey, srcDataKey, dstDs, dstPathKey, dstDataKey); err != nil {
		fatal("copy: %v", err)
	}
}
