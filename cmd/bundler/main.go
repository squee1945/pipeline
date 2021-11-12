/*
Copyright 2021 The Tekton Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// This package provides a CLI tool for the creation of a Tekton bundle.
package main

import (
	"archive/tar"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"k8s.io/apimachinery/pkg/util/yaml"
)

const (
	maxResources = 10
)

var (
	help  = flag.Bool("help", false, "Show help.")
	image = flag.String("image", "", `The image to push, e.g., "gcr.io/example/my-bundle`)
)

func main() {
	flag.Parse()

	if *help {
		fmt.Println(usage())
		os.Exit(0)
	}

	if len(flag.Args()) == 0 {
		fmt.Println("Must provide at least one config file.")
		os.Exit(1)
	}

	if *image == "" {
		fmt.Println("--image is required.")
		os.Exit(1)
	}
	ref, err := name.ParseReference(*image)
	if err != nil {
		fmt.Printf("--image is invalid: %v", err)
		os.Exit(1)
	}

	resources, err := parseFiles(flag.Args())
	if err != nil {
		fmt.Printf("Error reading files: %v", err)
		os.Exit(1)
	}

	if len(resources) > maxResources {
		fmt.Printf("Too many resources, max %d, found %d.", maxResources, len(resources))
	}

	image, err := constructImage(resources)
	if err != nil {
		fmt.Printf("Error constructing image: %v", err)
		os.Exit(1)
	}

	digest, err := publishImage(image, ref)
	if err != nil {
		fmt.Printf("Error publishing image: %v", err)
		os.Exit(1)
	}

	fmt.Println(ref.Name() + "@" + digest)
}

func usage() string {
	return "bundler --image=<some-image, e.g., gcr.io/example.my-bundle> <config.yaml> [config.yaml]..."
}

type resourceKey struct {
	apiVersion string
	kind       string
	name       string
}

type resource struct {
	key     resourceKey
	content string
}

func parseFiles(filenames []string) ([]*resource, error) {
	var resources []*resource
	keys := make(map[resourceKey]bool)
	for _, filename := range filenames {
		contents, err := os.ReadFile(filename)
		if err != nil {
			return nil, fmt.Errorf("reading %q: %v", filename, err)
		}

		parts := strings.Split(string(contents), "---\n")

		for index, part := range parts {
			r, err := parseResource(part, filename, index)
			if err != nil {
				return nil, fmt.Errorf("parsing resource: %v", err)
			}
			fmt.Fprintf(os.Stderr, "Found %v (filename %s, index %d)\n", r.key, filename, index)
			if _, found := keys[r.key]; found {
				return nil, fmt.Errorf("duplicate entry %v", r.key)
			}
			keys[r.key] = true
			resources = append(resources, r)
		}
	}
	return resources, nil
}

type YamlResource struct {
	Kind       string       `yaml:"kind"`
	ApiVersion string       `yaml:"apiVersion"`
	Metadata   YamlMetadata `yaml:"metadata"`
}

type YamlMetadata struct {
	Name string `yaml:"name"`
}

func parseResource(s, filename string, index int) (*resource, error) {
	var yr YamlResource
	if err := yaml.Unmarshal([]byte(s), &yr); err != nil {
		return nil, fmt.Errorf("unmarshalling yaml (filename %s, index %d): %v", filename, index, err)
	}

	if yr.ApiVersion != "tekton.dev/v1beta1" {
		return nil, fmt.Errorf("only tekton.dev/v1beta1 supported by this tool (filename %s, index %d)", filename, index)
	}

	switch yr.Kind {
	case "Task":
		// var t v1beta1.Task
		// if err := yaml.Unmarshal([]byte(s), &t); err != nil {
		// 	return nil, fmt.Errorf("unmarshalling Task (filename %s, index %d): %v", filename, index, err)
		// }
		// TODO: validate Task
	case "Pipeline":
		// var p v1beta1.Pipeline
		// if err := yaml.Unmarshal([]byte(s), &p); err != nil {
		// 	return nil, fmt.Errorf("unmarshalling Pipeline (filename %s, index %d): %v", filename, index, err)
		// }
		// TODO: validate Pipeline
	default:
		return nil, fmt.Errorf("unsupported Kind %q (filename %s, index %d)", yr.Kind, filename, index)
	}

	if yr.Metadata.Name == "" {
		return nil, fmt.Errorf("name is required (filename %s, index %d)", filename, index)
	}

	return &resource{
		key: resourceKey{
			apiVersion: yr.ApiVersion,
			kind:       yr.Kind,
			name:       yr.Metadata.Name,
		},
		content: s,
	}, nil
}

func constructImage(resources []*resource) (v1.Image, error) {
	tempDir := filepath.Join(os.TempDir(), "tekton-bundler-"+strconv.FormatInt(time.Now().UnixMicro(), 10))
	if err := os.MkdirAll(tempDir, 0700); err != nil {
		return nil, fmt.Errorf("making temp dir %s: %v", tempDir, err)
	}
	defer os.RemoveAll(tempDir)

	image := empty.Image
	for index, resource := range resources {
		tarname := filepath.Join(tempDir, fmt.Sprintf("resource-%d.tar", index))
		if err := createTarball(tarname, resource, index); err != nil {
			return nil, fmt.Errorf("creating tarball: %v", err)
		}

		annotations := map[string]string{
			"dev.tekton.image.apiVersion": resource.key.apiVersion,
			"dev.tekton.image.kind":       resource.key.kind,
			"dev.tekton.image.name":       resource.key.name,
		}

		var err error
		layer, err := tarball.LayerFromFile(tarname, tarball.WithAnnotations(annotations))
		if err != nil {
			return nil, fmt.Errorf("creating layer: %v", err)
		}

		image, err = mutate.AppendLayers(image, layer)
		if err != nil {
			return nil, fmt.Errorf("appending layer: %v", err)
		}
	}
	return image, nil
}

func createTarball(name string, res *resource, index int) error {
	tarfile, err := os.Create(name)
	if err != nil {
		return err
	}
	defer tarfile.Close()
	tw := tar.NewWriter(tarfile)

	hdr := &tar.Header{
		Name: fmt.Sprintf("resource-%d.yaml", index),
		Mode: 0600,
		Size: int64(len(res.content)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if _, err := tw.Write([]byte(res.content)); err != nil {
		return err
	}
	return nil
}

func publishImage(image v1.Image, ref name.Reference) (string, error) {
	fmt.Fprintf(os.Stderr, "Publishing image %s\n", ref)
	if err := crane.Push(image, ref.String()); err != nil {
		return "", fmt.Errorf("pushing image: %v", err)
	}
	digest, err := image.Digest()
	if err != nil {
		return "", fmt.Errorf("fetching digest: %v", err)
	}
	return digest.String(), nil
}
