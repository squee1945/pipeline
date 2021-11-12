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

	if len(os.Args) == 0 {
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

	files := os.Args[1:]
	resources, err := parseFiles(files)
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

type resource struct {
	name       string
	kind       string
	apiVersion string
	content    string
}

func parseFiles(filenames []string) ([]*resource, error) {
	var resources []*resource
	for _, filename := range filenames {
		contents, err := os.ReadFile(filename)
		if err != nil {
			return nil, fmt.Errorf("reading %q: %v", filename, err)
		}

		parts := strings.Split(string(contents), "---\n")

		for index, part := range parts {
			resource, err := parseResource(part, filename, index)
			if err != nil {
				name := partName(part)
				return nil, fmt.Errorf("parsing part: %v\n%s", err, name)
			}
			resources = append(resources, resource)
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
		name:       yr.Metadata.Name,
		kind:       yr.Kind,
		apiVersion: yr.ApiVersion,
		content:    s,
	}, nil
}

func partName(part string) string {
	if len(part) < 300 {
		return part
	}
	return part[:300]
}

func constructImage(resources []*resource) (v1.Image, error) {
	tempDir := filepath.Join(os.TempDir(), "tekton-bundler-"+strconv.FormatInt(time.Now().UnixMicro(), 10))
	if err := os.MkdirAll(tempDir, 0700); err != nil {
		return nil, fmt.Errorf("making temp dir %s: %v", tempDir, err)
	}
	defer os.RemoveAll(tempDir)

	image := empty.Image
	for i, resource := range resources {
		fname := fmt.Sprintf("resource-%d.yaml", i)
		if err := os.WriteFile(fname, []byte(resource.content), 0600); err != nil {
			return nil, fmt.Errorf("writing file %s: %v", fname, err)
		}
		var err error
		image, err = crane.Append(image, fname)
		if err != nil {
			return nil, fmt.Errorf("appending layer: %v", err)
		}
		annotations := map[string]string{
			"dev.tekton.image.name":       resource.name,
			"dev.tekton.image.kind":       resource.kind,
			"dev.tekton.image.apiVersion": resource.apiVersion,
		}
		image = mutate.Annotations(image, annotations).(v1.Image)
	}
	return image, nil
}

func publishImage(image v1.Image, ref name.Reference) (string, error) {
	if err := crane.Push(image, ref.String()); err != nil {
		return "", fmt.Errorf("pushing image: %v", err)
	}
	digest, err := image.Digest()
	if err != nil {
		return "", fmt.Errorf("fetching digest: %v", err)
	}
	return digest.String(), nil
}
