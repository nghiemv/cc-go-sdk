package cc

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/usace-cloud-compute/filesapi"
)

type DataSourceIoType string

const (
	DataSourceInput  DataSourceIoType = "INPUT"
	DataSourceOutput DataSourceIoType = "OUTPUT"
	DataSourceAll    DataSourceIoType = "" //zero value == all
)

type Payload struct {
	IOManager
	Actions []Action `json:"actions"`
}

type Action struct {
	IOManager
	Type        string `json:"type,omitempty"`
	Description string `json:"description,omitempty"`
	Name        string `json:"name,omitempty"`
}

// -----------------------------------------------
// Wrapped IOManager functions
// -----------------------------------------------

func (a Action) GetStore(name string) (*DataStore, error) {
	return a.IOManager.GetStore(name)
}

func (a Action) GetDataSource(input GetDsInput) (DataSource, error) {
	return a.IOManager.GetDataSource(input)
}

func (a Action) GetInputDataSource(name string) (DataSource, error) {
	return a.IOManager.GetInputDataSource(name)
}

func (a Action) GetOutputDataSource(name string) (DataSource, error) {
	return a.IOManager.GetOutputDataSource(name)
}

func (a Action) GetReader(input DataSourceOpInput) (io.ReadCloser, error) {
	return a.IOManager.GetReader(input)
}

func (a Action) Get(input DataSourceOpInput) ([]byte, error) {
	return a.IOManager.Get(input)
}

func (a Action) Put(input PutOpInput) (int, error) {
	return a.IOManager.Put(input)
}

func (a Action) Copy(src DataSourceOpInput, dest DataSourceOpInput) error {
	return a.IOManager.Copy(src, dest)
}

func (a Action) CopyFileToLocal(input CopyToLocalInput) error {
	return a.IOManager.CopyFileToLocal(input)
}

func (a Action) CopyFileToRemote(input CopyFileToRemoteInput) error {
	return a.IOManager.CopyFileToRemote(input)
}

// -----------------------------------------------
// IOManager
// -----------------------------------------------
type IOManager struct {
	Attributes PayloadAttributes `json:"attributes,omitempty"`
	Stores     []DataStore       `json:"stores"`
	Inputs     []DataSource      `json:"inputs"`
	Outputs    []DataSource      `json:"outputs"`
	parent     *IOManager
}

type GetDsInput struct {
	DsIoType DataSourceIoType
	DsName   string
}

type DataSourceOpInput struct {
	DataSource     *DataSource
	DataSourceName string
	PathKey        string
	DataPathKey    string
	TemplateVars   map[string]string
}

// used for key value template variable substitution
type KeyValue struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type PutOpInput struct {
	SrcReader io.Reader
	DataSourceOpInput
}

func (im *IOManager) SetParent(iom *IOManager) {
	im.parent = iom
}

func (im *IOManager) GetStore(name string) (*DataStore, error) {
	for _, store := range im.Stores {
		if store.Name == name {
			return &store, nil
		}
	}
	if im.parent != nil {
		return im.parent.GetStore(name)
	}

	return nil, fmt.Errorf("invalid store name: %s", name)
}

func (im *IOManager) GetDataSource(input GetDsInput) (DataSource, error) {
	sources := []DataSource{}
	switch input.DsIoType {
	case DataSourceInput:
		sources = im.Inputs
	case DataSourceOutput:
		sources = im.Outputs
	case DataSourceAll:
		sources = append(sources, im.Inputs...)
		sources = append(sources, im.Outputs...)
	}
	for _, ds := range sources {
		if input.DsName == ds.Name {
			return ds, nil
		}
	}
	if im.parent != nil {
		return im.parent.GetDataSource(input)
	}
	return DataSource{}, fmt.Errorf("data source %s not found", input.DsName)
}

func (im *IOManager) GetInputDataSource(name string) (DataSource, error) {
	return im.GetDataSource(GetDsInput{DataSourceInput, name})
}

func (im *IOManager) GetOutputDataSource(name string) (DataSource, error) {
	return im.GetDataSource(GetDsInput{DataSourceOutput, name})
}

func (im *IOManager) GetAbsolutePath(storename string, sourcename string, pathname string) (string, error) {
	store, err := im.GetStore(storename)
	if err != nil {
		return "", fmt.Errorf("failed to get store: %s", err)
	}

	ds, err := im.GetDataSource(GetDsInput{
		DsIoType: DataSourceAll,
		DsName:   sourcename,
	})

	if err != nil {
		return "", fmt.Errorf("failed to get data source: %s", err)
	}

	root := store.Parameters.GetStringOrDefault("root", "/")
	if path, ok := ds.Paths[pathname]; ok {
		return filepath.Clean(fmt.Sprintf("%s%c%s", root, os.PathSeparator, path)), nil
	}
	return "", fmt.Errorf("invalid path name: %s", pathname)

}

func (im *IOManager) GetReader(input DataSourceOpInput) (io.ReadCloser, error) {
	var err error
	var dataSource DataSource
	if input.DataSource == nil {
		dataSource, err = im.GetInputDataSource(input.DataSourceName)
		if err != nil {
			return nil, err
		}
	} else {
		dataSource = *input.DataSource
	}

	dataStore, err := im.GetStore(dataSource.StoreName)
	if err != nil {
		return nil, err
	}
	if readerStore, ok := dataStore.Session.(StoreReader); ok {
		path := dataSource.Paths[input.PathKey]
		if len(input.TemplateVars) > 0 {
			path = templateVarSubstitution(path, input.TemplateVars)
		}
		datapath := ""
		if input.DataPathKey != "" {
			datapath = dataSource.DataPaths[input.DataPathKey]
		}
		return readerStore.Get(path, datapath)
	}
	return nil, fmt.Errorf("data store %s session does not implement a StoreReader", dataStore.Name)
}

func (im *IOManager) Get(input DataSourceOpInput) ([]byte, error) {
	reader, err := im.GetReader(input)
	if err != nil {
		return nil, err
	}

	buf := new(bytes.Buffer)
	buf.ReadFrom(reader)
	return buf.Bytes(), nil
}

func (im *IOManager) Put(input PutOpInput) (int, error) {
	var err error
	var ds DataSource

	if input.DataSource == nil {
		ds, err = im.GetOutputDataSource(input.DataSourceName)
		if err != nil {
			return 0, err
		}
	}

	store, err := im.GetStore(ds.StoreName)
	if err != nil {
		return 0, err
	}

	if writer, ok := store.Session.(StoreWriter); ok {
		if path, ok := ds.Paths[input.PathKey]; ok {
			if len(input.TemplateVars) > 0 {
				path = templateVarSubstitution(path, input.TemplateVars)
			}
			datapath := ""
			if input.DataPathKey != "" {
				var dpok bool
				if datapath, dpok = ds.DataPaths[input.DataPathKey]; !dpok {
					return 0, fmt.Errorf("expected data source data path %s not found", input.DataPathKey)
				}
			}
			return writer.Put(input.SrcReader, path, datapath)
		}
		return 0, fmt.Errorf("data source path %s not found", input.PathKey)
	}
	return 0, fmt.Errorf("data store %s session does not implement a storewriter", ds.StoreName)
}

func (im *IOManager) Copy(src DataSourceOpInput, dest DataSourceOpInput) error {
	srcds, err := im.GetOutputDataSource(src.DataSourceName)
	if err != nil {
		return err
	}

	srcstore, err := im.GetStore(srcds.StoreName)
	if err != nil {
		return err
	}

	destds, err := im.GetOutputDataSource(dest.DataSourceName)
	if err != nil {
		return err
	}

	deststore, err := im.GetStore(destds.StoreName)
	if err != nil {
		return err
	}

	if srcReader, ok := srcstore.Session.(StoreReader); ok {
		if destwriter, ok := deststore.Session.(StoreWriter); ok {

			//get the reader
			srcpath := srcds.Paths[src.PathKey]
			srcdatapath := ""
			if src.DataPathKey != "" {
				srcdatapath = srcds.DataPaths[src.DataPathKey]
			}
			reader, err := srcReader.Get(srcpath, srcdatapath)
			if err != nil {
				return err
			}

			//write
			destpath := destds.Paths[dest.PathKey]
			destdatapath := ""
			if dest.DataPathKey != "" {
				destdatapath = destds.DataPaths[dest.DataPathKey]
			}
			_, err = destwriter.Put(reader, destpath, destdatapath)
			return err
		}
		return fmt.Errorf("destination data store %s session does not implement a storewriter", srcstore.Name)
	}
	return fmt.Errorf("source data store %s session does not implement a storereader", srcstore.Name)
}

type CopyToLocalInput struct {
	DsName    string
	PathKey   string
	LocalPath string
}

func (im *IOManager) CopyFileToLocal(input CopyToLocalInput) error {
	ds, err := im.GetDataSource(GetDsInput{DataSourceInput, input.DsName})
	if err != nil {
		return err
	}

	store, err := im.GetStore(ds.StoreName)
	if err != nil {
		return err
	}

	relativePath, ok := ds.Paths[input.PathKey]
	if !ok {
		return fmt.Errorf("pathkey '%s' not found", input.PathKey)
	}

	ifds, ok := store.Session.(FileDataStoreInterface)
	if !ok {
		return fmt.Errorf("data store %s is not a filestore", input.DsName)
	}

	fullpath := ifds.GetAbsolutePath(relativePath)
	fstore := ifds.GetFilestore()

	_, err = fstore.GetObjectInfo(filesapi.PathConfig{Path: fullpath})
	if err == nil {
		//file exists...copy it
		return copyToLocal(fstore, fullpath, input.LocalPath, false)
	}

	//assume its a dir/prefix and attempt to walk
	storeRoot := ""
	if sr, ok := store.Parameters["root"]; ok {
		if srstring, ok := sr.(string); ok {
			storeRoot = strings.TrimLeft(srstring, "/")
		}
	}

	//walk the directory structure
	return fstore.Walk(filesapi.WalkInput{
		Path: filesapi.PathConfig{Path: fullpath},
	}, func(path string, file os.FileInfo) error {
		f := file.Name()
		rootPrefix := fmt.Sprintf("%s/%s", storeRoot, relativePath)
		localPath := strings.Replace(f, rootPrefix, input.LocalPath, 1)
		return copyToLocal(fstore, f, localPath, true)
	})
}

func copyToLocal(fs filesapi.FileStore, remoteAbsolutePath string, localPath string, isdir bool) error {
	reader, err := fs.GetObject(filesapi.GetObjectInput{
		Path: filesapi.PathConfig{Path: remoteAbsolutePath},
	})
	if err != nil {
		return err
	}
	defer reader.Close()

	if isdir {
		localPath = filepath.Dir(localPath)
	}
	err = os.MkdirAll(localPath, 0755)
	if err != nil {
		return fmt.Errorf("failed to create local directory %s: %s", localPath, err)
	}

	localfile := fmt.Sprintf("%s/%s", localPath, filepath.Base(remoteAbsolutePath))
	writer, err := os.Create(localfile)
	if err != nil {
		return err
	}
	defer writer.Close()
	_, err = io.Copy(writer, reader)
	return err
}

//	 the CopyFileToRemoteInput supports two possible configs
//	  1: using a store directly. add the following to the config:
//	     - RemoteStoreName
//		 - RemotePath //relative path from the store root to the resource
//		 - LocalPath //local path to copy
//	  2: using a DataSource.  Add the following to the config:
//	     - RemoteDsName: name of the remote data source you are copying to
//		 - DsPathKey: the datasource path key
//		 - DsDataPathKey: (optional) data path key if necessary
type CopyFileToRemoteInput struct {
	RemoteStoreName string //optional store name
	RemotePath      string
	LocalPath       string
	RemoteDsName    string
	DsPathKey       string
	DsDataPathKey   string
	TemplateVars    map[string]string
}

func (im *IOManager) CopyFileToRemote(input CopyFileToRemoteInput) error {
	storeName := input.RemoteStoreName
	path := input.RemotePath
	if storeName == "" {
		//get store name from datasource and use datasource semantics
		ds, err := im.GetDataSource(GetDsInput{DataSourceOutput, input.RemoteDsName})
		if err != nil {
			return err
		}
		storeName = ds.StoreName
		path = ds.Paths[input.DsPathKey]
	}

	for k, valStr := range input.TemplateVars {
		varStr := fmt.Sprintf("{VAR::%s}", k)
		path = strings.ReplaceAll(path, varStr, valStr)
	}

	store, err := im.GetStore(storeName)
	if err != nil {
		return err
	}

	info, err := os.Stat(input.LocalPath)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("Path '%s' does not exist.\n", path)
			return err
		}
		fmt.Printf("Error getting file info for '%s': %v\n", path, err)
		return err
	}

	if ifds, ok := store.Session.(FileDataStoreInterface); ok {
		fullRemotePath := ifds.GetAbsolutePath(path)
		fs := ifds.GetFilestore()
		if info.IsDir() {
			localFs, err := filesapi.NewFileStore(filesapi.BlockFSConfig{})
			if err != nil {
				return err
			}
			localFs.Walk(filesapi.WalkInput{
				Path: filesapi.PathConfig{Path: input.LocalPath},
			}, func(path string, file os.FileInfo) error {
				if !file.IsDir() {
					localRelativePath := strings.TrimPrefix(strings.TrimPrefix(path, input.LocalPath), "/")
					fullRemoteFilePath := fmt.Sprintf("%s/%s", fullRemotePath, localRelativePath)
					return writeFileToRemote(fs, path, fullRemoteFilePath)
				}
				return nil
			})
		} else {
			return writeFileToRemote(fs, input.LocalPath, fullRemotePath)
		}
	}
	return nil
}

func writeFileToRemote(fs filesapi.FileStore, localPath string, remoteAbsolutePath string) error {
	reader, err := os.Open(localPath)
	if err != nil {
		return err
	}

	// When the backing filestore is a BlockFS (FSB store type), the parent
	// directory for remoteAbsolutePath must exist before BlockFS.PutObject's
	// os.OpenFile(..., O_RDWR|O_CREATE) call, otherwise the open fails with
	// "no such file or directory". filesapi's BlockFS intentionally does not
	// MkdirAll on put, so we do it here for FSB-backed stores. S3FS does not
	// need this and must not be touched — remote keys are not local paths.
	if _, ok := fs.(*filesapi.BlockFS); ok {
		if err := os.MkdirAll(filepath.Dir(remoteAbsolutePath), 0o755); err != nil {
			return err
		}
	}

	_, err = fs.PutObject(filesapi.PutObjectInput{
		Source: filesapi.ObjectSource{
			Reader: reader,
		},
		Dest: filesapi.PathConfig{Path: remoteAbsolutePath},
	})
	return err
}

func GetStoreAs[T any](mgr *IOManager, name string) (T, error) {
	for _, s := range mgr.Stores {
		if s.Name == name {
			if t, ok := s.Session.(T); ok {
				return t, nil
			} else {
				return t, fmt.Errorf("invalid store type: %s", s.StoreType)
			}
		}
	}
	var t T
	return t, fmt.Errorf("session %s does not exist", name)
}
