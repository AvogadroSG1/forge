package scaffold

import (
	"errors"
	"io/fs"
	"os"
	"path"

	"forge/internal/project"
)

// File is one rendered output file of a template tree.
type File struct {
	Path string // destination path relative to the repo root, slash-separated
	Data []byte
	Mode os.FileMode
}

// RenderAddon returns the files the agent add-on named by vars.Agent
// contributes for vars.Stack, in composition order: the add-on's common
// tree, then its stack tree. A later file with the same Path replaces the
// earlier one (the stack tree wins), so the result has unique paths.
// vars.Agent == "" returns nil, nil.
func RenderAddon(assets fs.FS, vars project.Variables) ([]File, error) {
	if vars.Agent == "" {
		return nil, nil
	}

	var files []File
	index := map[string]int{}
	noDir := func(string) error { return nil }
	collect := func(f File) error {
		if i, ok := index[f.Path]; ok {
			files[i] = f
			return nil
		}
		index[f.Path] = len(files)
		files = append(files, f)
		return nil
	}

	root := path.Join(addonsRoot, vars.Agent)
	if err := walkTree(assets, root+"/common", "", vars, nil, noDir, collect); err != nil {
		return nil, err
	}
	// Not every stack has stack-specific wiring.
	if err := walkTree(assets, root+"/"+vars.Stack, "", vars, nil, noDir, collect); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return files, nil
}
