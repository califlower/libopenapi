// Copyright 2023-2024 Princess Beef Heavy Industries, LLC / Dave Shanley
// https://pb33f.io
// SPDX-License-Identifier: MIT

package bundler

import (
	"context"
	"errors"
	"fmt"
	"gopkg.in/yaml.v3"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/pb33f/libopenapi"
	"github.com/pb33f/libopenapi/datamodel"
	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"
	"github.com/pb33f/libopenapi/index"
	"github.com/pb33f/libopenapi/orderedmap"
)

// ErrInvalidModel is returned when the model is not usable.
var ErrInvalidModel = errors.New("invalid model")

// BundleBytes will take a byte slice of an OpenAPI specification and return a bundled version of it.
// This is useful for when you want to take a specification with external references, and you want to bundle it
// into a single document.
//
// This function will 'resolve' all references in the specification and return a single document. The resulting
// document will be a valid OpenAPI specification, containing no references.
//
// Circular references will not be resolved and will be skipped.
func BundleBytes(bytes []byte, configuration *datamodel.DocumentConfiguration) ([]byte, error) {
	doc, err := libopenapi.NewDocumentWithConfiguration(bytes, configuration)
	if err != nil {
		return nil, err
	}

	v3Doc, errs := doc.BuildV3Model()
	err = errors.Join(errs...)
	if v3Doc == nil {
		return nil, errors.Join(ErrInvalidModel, err)
	}

	bundledBytes, e := bundle(&v3Doc.Model)
	return bundledBytes, errors.Join(err, e)
}

// BundleBytesComposed will take a byte slice of an OpenAPI specification and return a composed bundled version of it.
// this is the same as BundleBytes, but it will compose the bundling instead of inline it.
func BundleBytesComposed(bytes []byte, configuration *datamodel.DocumentConfiguration, compositionConfig *BundleCompositionConfig) ([]byte, error) {
	doc, err := libopenapi.NewDocumentWithConfiguration(bytes, configuration)
	if err != nil {
		return nil, err
	}

	v3Doc, errs := doc.BuildV3Model()
	err = errors.Join(errs...)
	if v3Doc == nil || len(errs) > 0 {
		return nil, errors.Join(ErrInvalidModel, err)
	}

	bundledBytes, e := compose(&v3Doc.Model, compositionConfig)
	return bundledBytes, errors.Join(err, e)
}

// BundleDocument will take a v3.Document and return a bundled version of it.
// This is useful for when you want to take a document that has been built
// from a specification with external references, and you want to bundle it
// into a single document.
//
// This function will 'resolve' all references in the specification and return a single document. The resulting
// document will be a valid OpenAPI specification, containing no references.
//
// Circular references will not be resolved and will be skipped.
func BundleDocument(model *v3.Document) ([]byte, error) {
	return bundle(model)
}

// BundleCompositionConfig is used to configure the composition of OpenAPI documents when using BundleDocumentComposed.
type BundleCompositionConfig struct {
	Delimiter string // Delimiter is used to separate clashing names. Defaults to `__`.
}

// BundleDocumentComposed will take a v3.Document and return a composed bundled version of it. Composed means
// that every external file will have references lifted out and added to the `components` section of the document.
// Names will be preserved where possible, conflicts will be appended with a number. If the type of the reference cannot
// be determined, it will be added to the `components` section as a `Schema` type, a warning will be logged.
// The document model will be mutated permanently.
//
// Circular references will not be resolved and will be skipped.
func BundleDocumentComposed(model *v3.Document, compositionConfig *BundleCompositionConfig) ([]byte, error) {
	return compose(model, compositionConfig)
}

func compose(model *v3.Document, compositionConfig *BundleCompositionConfig) ([]byte, error) {
	if compositionConfig == nil {
		compositionConfig = &BundleCompositionConfig{
			Delimiter: "__",
		}
	} else {
		if compositionConfig.Delimiter == "" {
			compositionConfig.Delimiter = "__"
		}
		if strings.Contains(compositionConfig.Delimiter, "#") ||
			strings.Contains(compositionConfig.Delimiter, "/") {
			return nil, errors.New("composition delimiter cannot contain '#' or '/' characters")
		}
		if strings.Contains(compositionConfig.Delimiter, " ") {
			return nil, errors.New("composition delimiter cannot contain spaces")
		}
	}

	if model == nil || model.Rolodex == nil {
		return nil, errors.New("model or rolodex is nil")
	}

	rolodex := model.Rolodex
	indexes := rolodex.GetIndexes()

	cf := &handleIndexConfig{
		idx:               rolodex.GetRootIndex(),
		model:             model,
		indexes:           indexes,
		seen:              sync.Map{},
		refMap:            orderedmap.New[string, *processRef](),
		compositionConfig: compositionConfig,
	}
	// recursive function to handle the indexes, we need a different approach to composition vs. inlining.
	handleIndex(cf)

	processedNodes := orderedmap.New[string, *processRef]()
	var errs []error
	for _, ref := range cf.refMap.FromOldest() {
		err := processReference(model, ref, cf)
		errs = append(errs, err)
		processedNodes.Set(ref.ref.FullDefinition, ref)
	}

	slices.SortFunc(indexes, func(i, j *index.SpecIndex) int {
		if i.GetSpecAbsolutePath() < j.GetSpecAbsolutePath() {
			return 1
		}
		return 0
	})

	rootIndex := rolodex.GetRootIndex()
	remapIndex(rootIndex, processedNodes)

	for _, idx := range indexes {
		remapIndex(idx, processedNodes)
	}

	// anything that could not be recomposed and needs inlining
	for _, pr := range cf.inlineRequired {
		if pr.refPointer != "" {

			// if the ref is a pointer to an external pointer, then we need to stitch it.
			uri := strings.Split(pr.refPointer, "#/")
			if len(uri) == 2 {
				if uri[0] != "" {
					if !filepath.IsAbs(uri[0]) && !strings.HasPrefix(uri[0], "http") {
						// if the uri is not absolute, then we need to make it absolute.
						uri[0] = filepath.Join(filepath.Dir(pr.idx.GetSpecAbsolutePath()), uri[0])
					}
					pointerRef := pr.idx.FindComponent(context.Background(), strings.Join(uri, "#/"))
					pr.seqRef.Node.Content = pointerRef.Node.Content
					continue
				}
			}
		}
		pr.seqRef.Node.Content = pr.ref.Node.Content
	}

	b, err := model.Render()
	errs = append(errs, err)

	return b, errors.Join(errs...)
}

func bundle(model *v3.Document) ([]byte, error) {
	rolodex := model.Rolodex
	indexes := rolodex.GetIndexes()
	preserveRefs := map[string]struct{}{}

	collectDiscriminatorMappingValues(rolodex.GetRootIndex(), rolodex.GetRootIndex().GetRootNode(), preserveRefs)
	for _, idx := range indexes {
		collectDiscriminatorMappingValues(idx, idx.GetRootNode(), preserveRefs)
	}

	// compact function.
	compact := func(idx *index.SpecIndex, root bool) {
		mappedReferences := idx.GetMappedReferences()
		sequencedReferences := idx.GetRawReferencesSequenced()
		for _, sequenced := range sequencedReferences {
			mappedReference := mappedReferences[sequenced.FullDefinition]

			// if we're in the root document, don't bundle anything.
			refExp := strings.Split(sequenced.FullDefinition, "#/")
			if len(refExp) == 2 {

				// make sure to use the correct index.
				// https://github.com/pb33f/libopenapi/issues/397
				if root {
					for _, i := range indexes {
						if i.GetSpecAbsolutePath() == refExp[0] {
							if mappedReference != nil && !mappedReference.Circular {
								mr := i.FindComponent(context.Background(), sequenced.Definition)
								if mr != nil {
									// found the component; this is the one we want to use.
									mappedReference = mr
									break
								}
							}
						}
					}
				}

				if refExp[0] == sequenced.Index.GetSpecAbsolutePath() || refExp[0] == "" {
					if root {
						idx.GetLogger().Debug("[bundler] skipping local root reference",
							"ref", sequenced.Definition)
						continue
					}
				}
			}

			if _, ok := preserveRefs[sequenced.FullDefinition]; ok {
				idx.GetLogger().Debug("[bundler] skipping union type (oneOf/anyOf) with discriminator mapping",
					"ref", sequenced.Definition)
				continue
			}

			if mappedReference != nil && !mappedReference.Circular {
				sequenced.Node.Content = mappedReference.Node.Content
				continue
			}

			if mappedReference != nil && mappedReference.Circular {
				if idx.GetLogger() != nil {
					idx.GetLogger().Warn("[bundler] skipping circular reference",
						"ref", sequenced.FullDefinition)
				}
			}
		}
	}

	for _, idx := range indexes {
		compact(idx, false)
	}
	compact(rolodex.GetRootIndex(), true)
	return model.Render()
}

func collectDiscriminatorMappingValues(idx *index.SpecIndex, n *yaml.Node, pinned map[string]struct{}) {
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		n = n.Content[0]
	}

	switch n.Kind {
	case yaml.SequenceNode:
		for _, c := range n.Content {
			collectDiscriminatorMappingValues(idx, c, pinned)
		}
		return
	case yaml.MappingNode:
	default:
		return
	}

	var discriminator, oneOf, anyOf *yaml.Node

	for i := 0; i < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		switch k.Value {
		case "discriminator":
			discriminator = v
		case "oneOf":
			oneOf = v
		case "anyOf":
			anyOf = v
		}
		collectDiscriminatorMappingValues(idx, v, pinned)
	}

	if discriminator != nil {
		walkDiscriminatorMapping(idx, discriminator, pinned)
		walkUnionRefs(idx, oneOf, pinned)
		walkUnionRefs(idx, anyOf, pinned)
	}
}

func walkDiscriminatorMapping(idx *index.SpecIndex, discriminatorNode *yaml.Node, pinned map[string]struct{}) {
	if discriminatorNode.Kind != yaml.MappingNode {
		return
	}

	for i := 0; i < len(discriminatorNode.Content); i += 2 {
		if discriminatorNode.Content[i].Value == "mapping" {
			mappingNode := discriminatorNode.Content[i+1]

			for j := 0; j < len(mappingNode.Content); j += 2 {
				refValue := mappingNode.Content[j+1].Value

				if ref, refIdx := idx.SearchIndexForReference(refValue); ref != nil {
					fullDef := fmt.Sprintf("%s%s", refIdx.GetSpecAbsolutePath(), ref.Definition)
					pinned[fullDef] = struct{}{}
				}
			}
		}
	}
}

func walkUnionRefs(idx *index.SpecIndex, seq *yaml.Node, pinned map[string]struct{}) {
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return
	}
	for _, item := range seq.Content {
		if item.Kind != yaml.MappingNode {
			continue
		}
		for i := 0; i < len(item.Content); i += 2 {
			k, v := item.Content[i], item.Content[i+1]
			if k.Value != "$ref" || v.Kind != yaml.ScalarNode {
				continue
			}
			if ref, refIdx := idx.SearchIndexForReference(v.Value); ref != nil {
				full := fmt.Sprintf("%s%s", refIdx.GetSpecAbsolutePath(), ref.Definition)
				pinned[full] = struct{}{}
			}
		}
	}
}
