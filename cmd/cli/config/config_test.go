package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- GetConfigPath ---

func TestGetConfigPath_FromEnv(t *testing.T) {
	t.Setenv(EnvConfigPath, "/tmp/my-rbg-config")
	assert.Equal(t, "/tmp/my-rbg-config", GetConfigPath())
}

func TestGetConfigPath_Default(t *testing.T) {
	t.Setenv(EnvConfigPath, "")
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, DefaultConfigDir, DefaultConfigFile), GetConfigPath())
}

// --- loadFromFile ---

func TestLoadFromFile_NoFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvConfigPath, filepath.Join(dir, "nonexistent"))

	c := &Config{}
	err := c.loadFromFile()
	assert.NoError(t, err)
}

func TestLoadFromFile_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad")
	require.NoError(t, os.WriteFile(p, []byte(":::invalid"), 0600))
	t.Setenv(EnvConfigPath, p)

	c := &Config{}
	err := c.loadFromFile()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse config file")
}

func TestLoadFromFile_ValidYAML(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config")
	yaml := `apiVersion: rbg/v1alpha1
kind: Config
current-storage: my-storage
storages:
  - name: my-storage
    type: pvc
    config:
      pvcName: data-pvc
`
	require.NoError(t, os.WriteFile(p, []byte(yaml), 0600))
	t.Setenv(EnvConfigPath, p)

	c := &Config{}
	require.NoError(t, c.loadFromFile())
	assert.Equal(t, "rbg/v1alpha1", c.APIVersion)
	assert.Equal(t, "Config", c.Kind)
	assert.Equal(t, "my-storage", c.CurrentStorage)
	require.Len(t, c.Storages, 1)
	assert.Equal(t, "pvc", c.Storages[0].Type)
	assert.Equal(t, "data-pvc", c.Storages[0].Config["pvcName"])
}

// --- Save ---

func TestSave_WritesFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config")
	t.Setenv(EnvConfigPath, p)

	c := &Config{
		APIVersion: "rbg/v1alpha1",
		Kind:       "Config",
	}
	require.NoError(t, c.AddStorage("s1", "pvc", map[string]interface{}{"pvcName": "vol"}))
	require.NoError(t, c.Save())

	data, err := os.ReadFile(p)
	require.NoError(t, err)
	assert.Contains(t, string(data), "pvcName")
	assert.Contains(t, string(data), "s1")
}

func TestSave_CreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "sub", "dir", "config")
	t.Setenv(EnvConfigPath, nested)

	c := &Config{APIVersion: "rbg/v1alpha1", Kind: "Config"}
	require.NoError(t, c.Save())

	_, err := os.Stat(nested)
	assert.NoError(t, err)
}

func TestSave_EmptyPath(t *testing.T) {
	// Simulate inability to determine config path
	t.Setenv(EnvConfigPath, "")
	t.Setenv("HOME", "")

	c := &Config{}
	err := c.Save()
	// Either errors because path is empty or succeeds with the default;
	// we just confirm no panic.
	_ = err
}

// --- Load (fresh instance) ---

func TestLoad_ReadsUpdatedFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config")
	yaml1 := `apiVersion: rbg/v1alpha1
kind: Config`
	require.NoError(t, os.WriteFile(p, []byte(yaml1), 0600))
	t.Setenv(EnvConfigPath, p)

	instance = nil
	c, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "rbg/v1alpha1", c.APIVersion)

	yaml2 := `apiVersion: rbg/v1alpha2
kind: Config`
	require.NoError(t, os.WriteFile(p, []byte(yaml2), 0600))
	instance = nil
	c, err = Load()
	require.NoError(t, err)
	assert.Equal(t, "rbg/v1alpha2", c.APIVersion)
}

// --- Storage CRUD ---

func TestAddStorage_OK(t *testing.T) {
	c := &Config{}
	require.NoError(t, c.AddStorage("s1", "pvc", map[string]interface{}{"pvcName": "v1"}))
	assert.Len(t, c.Storages, 1)
	assert.Equal(t, "s1", c.CurrentStorage, "first storage should become current")
}

func TestAddStorage_AutoCurrent(t *testing.T) {
	c := &Config{}
	require.NoError(t, c.AddStorage("s1", "pvc", map[string]interface{}{"pvcName": "v1"}))
	require.NoError(t, c.AddStorage("s2", "pvc", map[string]interface{}{"pvcName": "v2"}))
	assert.Equal(t, "s1", c.CurrentStorage, "should stay on first")
}

func TestAddStorage_Duplicate(t *testing.T) {
	c := &Config{}
	require.NoError(t, c.AddStorage("s1", "pvc", nil))
	err := c.AddStorage("s1", "pvc", nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestGetStorage_OK(t *testing.T) {
	c := &Config{}
	require.NoError(t, c.AddStorage("s1", "pvc", map[string]interface{}{"pvcName": "v1"}))
	s, err := c.GetStorage("s1")
	require.NoError(t, err)
	assert.Equal(t, "pvc", s.Type)
}

func TestGetStorage_NotFound(t *testing.T) {
	c := &Config{}
	_, err := c.GetStorage("nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestUpdateStorage_OK(t *testing.T) {
	c := &Config{}
	require.NoError(t, c.AddStorage("s1", "pvc", map[string]interface{}{"pvcName": "old"}))
	require.NoError(t, c.UpdateStorage("s1", map[string]interface{}{"pvcName": "new"}))
	s, _ := c.GetStorage("s1")
	assert.Equal(t, "new", s.Config["pvcName"])
}

func TestUpdateStorage_NotFound(t *testing.T) {
	c := &Config{}
	err := c.UpdateStorage("nope", nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestDeleteStorage_OK(t *testing.T) {
	c := &Config{}
	require.NoError(t, c.AddStorage("s1", "pvc", nil))
	require.NoError(t, c.AddStorage("s2", "pvc", nil))
	require.NoError(t, c.DeleteStorage("s2"))
	assert.Len(t, c.Storages, 1)
}

func TestDeleteStorage_Current(t *testing.T) {
	c := &Config{}
	require.NoError(t, c.AddStorage("s1", "pvc", nil))
	err := c.DeleteStorage("s1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot delete current")
}

func TestDeleteStorage_NotFound(t *testing.T) {
	c := &Config{}
	err := c.DeleteStorage("nope")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestUseStorage_OK(t *testing.T) {
	c := &Config{}
	require.NoError(t, c.AddStorage("s1", "pvc", nil))
	require.NoError(t, c.AddStorage("s2", "pvc", nil))
	require.NoError(t, c.UseStorage("s2"))
	assert.Equal(t, "s2", c.CurrentStorage)
}

func TestUseStorage_NotFound(t *testing.T) {
	c := &Config{}
	err := c.UseStorage("nope")
	assert.Error(t, err)
}

func TestGetCurrentStorageConfig_OK(t *testing.T) {
	c := &Config{}
	require.NoError(t, c.AddStorage("s1", "pvc", map[string]interface{}{"pvcName": "v1"}))
	cfg, err := c.GetCurrentStorageConfig()
	require.NoError(t, err)
	assert.Equal(t, "v1", cfg["pvcName"])
}

func TestGetCurrentStorageConfig_NoCurrent(t *testing.T) {
	c := &Config{}
	_, err := c.GetCurrentStorageConfig()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no storage configured")
}

// --- Source CRUD ---

func TestAddSource_OK(t *testing.T) {
	c := &Config{}
	require.NoError(t, c.AddSource("src1", "huggingface", nil))
	assert.Len(t, c.Sources, 1)
	assert.Equal(t, "src1", c.CurrentSource)
}

func TestAddSource_AutoCurrent(t *testing.T) {
	c := &Config{}
	require.NoError(t, c.AddSource("src1", "huggingface", nil))
	require.NoError(t, c.AddSource("src2", "modelscope", nil))
	assert.Equal(t, "src1", c.CurrentSource)
}

func TestAddSource_Duplicate(t *testing.T) {
	c := &Config{}
	require.NoError(t, c.AddSource("src1", "huggingface", nil))
	err := c.AddSource("src1", "huggingface", nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestGetSource_OK(t *testing.T) {
	c := &Config{}
	require.NoError(t, c.AddSource("src1", "huggingface", map[string]interface{}{"token": "t"}))
	s, err := c.GetSource("src1")
	require.NoError(t, err)
	assert.Equal(t, "huggingface", s.Type)
}

func TestGetSource_NotFound(t *testing.T) {
	c := &Config{}
	_, err := c.GetSource("nope")
	assert.Error(t, err)
}

func TestUpdateSource_OK(t *testing.T) {
	c := &Config{}
	require.NoError(t, c.AddSource("src1", "huggingface", map[string]interface{}{"token": "old"}))
	require.NoError(t, c.UpdateSource("src1", map[string]interface{}{"token": "new"}))
	s, _ := c.GetSource("src1")
	assert.Equal(t, "new", s.Config["token"])
}

func TestUpdateSource_NotFound(t *testing.T) {
	c := &Config{}
	err := c.UpdateSource("nope", nil)
	assert.Error(t, err)
}

func TestDeleteSource_OK(t *testing.T) {
	c := &Config{}
	require.NoError(t, c.AddSource("src1", "huggingface", nil))
	require.NoError(t, c.AddSource("src2", "modelscope", nil))
	require.NoError(t, c.DeleteSource("src2"))
	assert.Len(t, c.Sources, 1)
}

func TestDeleteSource_Current(t *testing.T) {
	c := &Config{}
	require.NoError(t, c.AddSource("src1", "huggingface", nil))
	err := c.DeleteSource("src1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot delete current")
}

func TestDeleteSource_NotFound(t *testing.T) {
	c := &Config{}
	err := c.DeleteSource("nope")
	assert.Error(t, err)
}

func TestUseSource_OK(t *testing.T) {
	c := &Config{}
	require.NoError(t, c.AddSource("src1", "huggingface", nil))
	require.NoError(t, c.AddSource("src2", "modelscope", nil))
	require.NoError(t, c.UseSource("src2"))
	assert.Equal(t, "src2", c.CurrentSource)
}

func TestUseSource_NotFound(t *testing.T) {
	c := &Config{}
	err := c.UseSource("nope")
	assert.Error(t, err)
}

func TestGetCurrentSourceConfig_OK(t *testing.T) {
	c := &Config{}
	require.NoError(t, c.AddSource("src1", "huggingface", map[string]interface{}{"token": "tk"}))
	cfg, err := c.GetCurrentSourceConfig()
	require.NoError(t, err)
	assert.Equal(t, "tk", cfg["token"])
}

func TestGetCurrentSourceConfig_NoCurrent(t *testing.T) {
	c := &Config{}
	_, err := c.GetCurrentSourceConfig()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no source configured")
}

// --- Engine CRUD ---

func TestSetEngine_Create(t *testing.T) {
	c := &Config{}
	c.SetEngine("vllm", nil)
	assert.Len(t, c.Engines, 1)
}

func TestSetEngine_Upsert(t *testing.T) {
	c := &Config{}
	c.SetEngine("vllm", map[string]interface{}{"image": "old"})
	c.SetEngine("vllm", map[string]interface{}{"image": "new"})
	// should still be one entry, not two
	assert.Len(t, c.Engines, 1)
	e, err := c.GetEngine("vllm")
	require.NoError(t, err)
	assert.Equal(t, "new", e.Config["image"])
}

func TestGetEngine_OK(t *testing.T) {
	c := &Config{}
	c.SetEngine("vllm", map[string]interface{}{"image": "v"})
	e, err := c.GetEngine("vllm")
	require.NoError(t, err)
	assert.Equal(t, "vllm", e.Type)
}

func TestGetEngine_NotFound(t *testing.T) {
	c := &Config{}
	_, err := c.GetEngine("nonexistent")
	assert.Error(t, err)
}

func TestDeleteEngine_OK(t *testing.T) {
	c := &Config{}
	c.SetEngine("vllm", nil)
	c.SetEngine("sglang", nil)
	require.NoError(t, c.DeleteEngine("sglang"))
	assert.Len(t, c.Engines, 1)
}

func TestDeleteEngine_NotFound(t *testing.T) {
	c := &Config{}
	err := c.DeleteEngine("nonexistent")
	assert.Error(t, err)
}

// --- Round-trip: Save then Load ---

func TestSaveAndReload(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config")
	t.Setenv(EnvConfigPath, p)

	c := &Config{
		APIVersion: "rbg/v1alpha1",
		Kind:       "Config",
	}
	require.NoError(t, c.AddStorage("s1", "pvc", map[string]interface{}{"pvcName": "v1"}))
	require.NoError(t, c.AddSource("src1", "huggingface", map[string]interface{}{"token": "tk"}))
	c.SetEngine("vllm", map[string]interface{}{"image": "img"})
	require.NoError(t, c.Save())

	instance = nil
	loaded, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "s1", loaded.CurrentStorage)
	assert.Equal(t, "src1", loaded.CurrentSource)
	require.Len(t, loaded.Storages, 1)
	require.Len(t, loaded.Sources, 1)
	require.Len(t, loaded.Engines, 1)
	assert.Equal(t, "v1", loaded.Storages[0].Config["pvcName"])
	assert.Equal(t, "tk", loaded.Sources[0].Config["token"])
	require.Len(t, loaded.Engines, 1)
	assert.Equal(t, "vllm", loaded.Engines[0].Type)
	assert.Equal(t, "img", loaded.Engines[0].Config["image"])
}
