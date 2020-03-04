/*
 * Copyright 2018-2020 the original author or authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *      https://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package libpak

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/buildpacks/libcnb"
	"github.com/heroku/color"
	"github.com/mitchellh/mapstructure"
	"github.com/paketoio/libpak/bard"
)

// LayerContributor is a helper for implementing a libcnb.LayerContributor in order to get consistent logging and
// avoidance.
type LayerContributor struct {

	// ExpectedMetadata is the metadata to compare against any existing layer metadata.
	ExpectedMetadata map[string]interface{}

	// Layer is the layer being contributed.
	Layer libcnb.Layer

	// Name is the user readable name of the contribution.
	Name string

	logger bard.Logger
}

// NewLayerContributor creates a new instance.
func NewLayerContributor(name string, expectedMetadata map[string]interface{}, layer libcnb.Layer) LayerContributor {
	return LayerContributor{
		ExpectedMetadata: expectedMetadata,
		Layer:            layer,
		Name:             name,

		logger: bard.NewLogger(os.Stdout),
	}
}

// LayerFunc is a callback function that is invoked when a layer needs to be contributed.
type LayerFunc func(layer libcnb.Layer) (libcnb.Layer, error)

// Contribute is the function to call when implementing your libcnb.LayerContributor.
func (l *LayerContributor) Contribute(f LayerFunc) (libcnb.Layer, error) {
	expected := reflect.New(reflect.TypeOf(l.ExpectedMetadata))
	expected.Elem().Set(reflect.ValueOf(l.ExpectedMetadata))

	actual := reflect.New(reflect.TypeOf(l.ExpectedMetadata)).Interface()
	if err := mapstructure.Decode(l.Layer.Metadata, &actual); err != nil {
		return libcnb.Layer{}, fmt.Errorf("unable to decode metadata into %s: %w", reflect.TypeOf(l.ExpectedMetadata), err)
	}

	if reflect.DeepEqual(expected.Interface(), actual) {
		l.logger.Header("%s: %s cached layer", color.BlueString(l.Name), color.GreenString("Reusing"))
		return l.Layer, nil
	}

	l.logger.Header("%s: %s to layer", color.BlueString(l.Name), color.YellowString("Contributing"))

	if err := os.RemoveAll(l.Layer.Path); err != nil {
		return libcnb.Layer{}, fmt.Errorf("unable to remove existing layer directory %s: %w", l.Layer.Path, err)
	}

	if err := os.MkdirAll(l.Layer.Path, 0755); err != nil {
		return libcnb.Layer{}, fmt.Errorf("unable to create layer directory %s: %w", l.Layer.Path, err)
	}

	layer, err := f(l.Layer)
	if err != nil {
		return libcnb.Layer{}, err
	}

	if err := mapstructure.Decode(l.ExpectedMetadata, &layer.Metadata); err != nil {
		return libcnb.Layer{}, fmt.Errorf("unable to encode metadata into %+v: %w", l.ExpectedMetadata, err)
	}

	return layer, nil
}

// DependencyLayerContributor is a helper for implementing a libcnb.LayerContributor for a BuildpackDependency in order
// to get consistent logging and avoidance.
type DependencyLayerContributor struct {

	// Dependency is the dependency being contributed.
	Dependency BuildpackDependency

	// DependencyCache is the cache to use to get the dependency.
	DependencyCache DependencyCache

	// LayerContributor is the contained LayerContributor used for the actual contribution.
	LayerContributor LayerContributor
}

// NewDependencyLayerContributor creates a new instance and adds the dependency to the Buildpack Plan.
func NewDependencyLayerContributor(dependency BuildpackDependency, cache DependencyCache, layer libcnb.Layer,
	plan *libcnb.BuildpackPlan) DependencyLayerContributor {

	plan.Entries = append(plan.Entries, libcnb.BuildpackPlanEntry{
		Name:    dependency.ID,
		Version: dependency.Version,
		Metadata: map[string]interface{}{
			"name":     dependency.Name,
			"uri":      dependency.URI,
			"sha256":   dependency.SHA256,
			"stacks":   dependency.Stacks,
			"licenses": dependency.Licenses,
		},
	})

	expected := map[string]interface{}{
		"id":       dependency.ID,
		"name":     dependency.Name,
		"version":  dependency.Version,
		"uri":      dependency.URI,
		"sha256":   dependency.SHA256,
		"stacks":   dependency.Stacks,
		"licenses": []map[string]interface{}{},
	}
	for _, l := range dependency.Licenses {
		expected["licenses"] = append(expected["licenses"].([]map[string]interface{}), map[string]interface{}{
			"type": l.Type,
			"uri":  l.URI,
		})
	}

	return DependencyLayerContributor{
		Dependency:       dependency,
		DependencyCache:  cache,
		LayerContributor: NewLayerContributor(fmt.Sprintf("%s %s", dependency.Name, dependency.Version), expected, layer),
	}
}

// DependencyLayerFunc is a callback function that is invoked when a dependency needs to be contributed.
type DependencyLayerFunc func(artifact *os.File, layer libcnb.Layer) (libcnb.Layer, error)

// Contribute is the function to call whe implementing your libcnb.LayerContributor.
func (d *DependencyLayerContributor) Contribute(f DependencyLayerFunc) (libcnb.Layer, error) {
	return d.LayerContributor.Contribute(func(layer libcnb.Layer) (libcnb.Layer, error) {
		artifact, err := d.DependencyCache.Artifact(d.Dependency)
		if err != nil {
			return libcnb.Layer{}, fmt.Errorf("unable to get dependency %s: %w", d.Dependency.ID, err)
		}

		return f(artifact, layer)
	})
}

// HelperLayerContributor is a helper for implementing a libcnb.LayerContributor for a buildpack helper application in
// order to get consistent logging and avoidance.
type HelperLayerContributor struct {

	// Path is the path to the helper application.
	Path string

	// LayerContributor is the contained LayerContributor used for the actual contribution.
	LayerContributor LayerContributor
}

// NewHelperLayerContributor creates a new instance and adds the helper to the Buildpack Plan.
func NewHelperLayerContributor(path string, name string, info libcnb.BuildpackInfo, layer libcnb.Layer,
	plan *libcnb.BuildpackPlan) HelperLayerContributor {

	plan.Entries = append(plan.Entries, libcnb.BuildpackPlanEntry{
		Name:    filepath.Base(path),
		Version: info.Version,
		Metadata: map[string]interface{}{
			"id":      info.ID,
			"version": info.Version,
		},
	})

	expected := map[string]interface{}{
		"id":        info.ID,
		"name":      info.Name,
		"version":   info.Version,
		"clear-env": info.ClearEnvironment,
	}

	return HelperLayerContributor{
		Path:             path,
		LayerContributor: NewLayerContributor(fmt.Sprintf("%s %s", name, info.Version), expected, layer),
	}
}

// DependencyLayerFunc is a callback function that is invoked when a helper needs to be contributed.
type HelperLayerFunc func(artifact *os.File, layer libcnb.Layer) (libcnb.Layer, error)

// Contribute is the function to call whe implementing your libcnb.LayerContributor.
func (h *HelperLayerContributor) Contribute(f HelperLayerFunc) (libcnb.Layer, error) {
	return h.LayerContributor.Contribute(func(layer libcnb.Layer) (libcnb.Layer, error) {
		in, err := os.Open(h.Path)
		if err != nil {
			return libcnb.Layer{}, fmt.Errorf("unable to open %s: %w", h.Path, err)
		}

		return f(in, layer)
	})
}