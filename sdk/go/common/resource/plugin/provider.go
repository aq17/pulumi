// Copyright 2016-2021, Pulumi Corporation.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package plugin

import (
	"errors"
	"fmt"
	"io"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/config"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
	"github.com/pulumi/pulumi/sdk/v3/go/common/util/contract"
	"github.com/pulumi/pulumi/sdk/v3/go/common/workspace"
)

// Provider presents a simple interface for orchestrating resource create, read, update, and delete operations.  Each
// provider understands how to handle all of the resource types within a single package.
//
// This interface hides some of the messiness of the underlying machinery, since providers are behind an RPC boundary.
//
// It is important to note that provider operations are not transactional.  (Some providers might decide to offer
// transactional semantics, but such a provider is a rare treat.)  As a result, failures in the operations below can
// range from benign to catastrophic (possibly leaving behind a corrupt resource).  It is up to the provider to make a
// best effort to ensure catastrophes do not occur.  The errors returned from mutating operations indicate both the
// underlying error condition in addition to a bit indicating whether the operation was successfully rolled back.
type Provider interface {
	// Closer closes any underlying OS resources associated with this provider (like processes, RPC channels, etc).
	io.Closer
	// Pkg fetches this provider's package.
	Pkg() tokens.Package

	// GetSchema returns the schema for the provider.
	GetSchema(version int) ([]byte, error)

	// CheckConfig validates the configuration for this resource provider.
	CheckConfig(urn resource.URN, olds, news resource.PropertyMap,
		allowUnknowns bool) (resource.PropertyMap, []CheckFailure, error)
	// DiffConfig checks what impacts a hypothetical change to this provider's configuration will have on the provider.
	DiffConfig(urn resource.URN, olds, news resource.PropertyMap, allowUnknowns bool,
		ignoreChanges []string) (DiffResult, error)
	// Configure configures the resource provider with "globals" that control its behavior.
	Configure(inputs resource.PropertyMap) error

	// Check validates that the given property bag is valid for a resource of the given type and returns the inputs
	// that should be passed to successive calls to Diff, Create, or Update for this resource.
	Check(urn resource.URN, olds, news resource.PropertyMap,
		allowUnknowns bool, randomSeed []byte) (resource.PropertyMap, []CheckFailure, error)
	// Diff checks what impacts a hypothetical update will have on the resource's properties.
	Diff(urn resource.URN, id resource.ID, olds resource.PropertyMap, news resource.PropertyMap,
		allowUnknowns bool, ignoreChanges []string) (DiffResult, error)
	// Create allocates a new instance of the provided resource and returns its unique resource.ID.
	Create(urn resource.URN, news resource.PropertyMap, timeout float64, preview bool) (resource.ID,
		resource.PropertyMap, resource.Status, error)
	// Read the current live state associated with a resource.  Enough state must be include in the inputs to uniquely
	// identify the resource; this is typically just the resource ID, but may also include some properties.  If the
	// resource is missing (for instance, because it has been deleted), the resulting property map will be nil.
	Read(urn resource.URN, id resource.ID,
		inputs, state resource.PropertyMap) (ReadResult, resource.Status, error)
	// Update updates an existing resource with new values.
	Update(urn resource.URN, id resource.ID,
		olds resource.PropertyMap, news resource.PropertyMap, timeout float64,
		ignoreChanges []string, preview bool) (resource.PropertyMap, resource.Status, error)
	// Delete tears down an existing resource.
	Delete(urn resource.URN, id resource.ID, props resource.PropertyMap, timeout float64) (resource.Status, error)

	// Construct creates a new component resource.
	Construct(info ConstructInfo, typ tokens.Type, name tokens.QName, parent resource.URN, inputs resource.PropertyMap,
		options ConstructOptions) (ConstructResult, error)

	// Invoke dynamically executes a built-in function in the provider.
	Invoke(tok tokens.ModuleMember, args resource.PropertyMap) (resource.PropertyMap, []CheckFailure, error)
	// StreamInvoke dynamically executes a built-in function in the provider, which returns a stream
	// of responses.
	StreamInvoke(
		tok tokens.ModuleMember,
		args resource.PropertyMap,
		onNext func(resource.PropertyMap) error) ([]CheckFailure, error)
	// Call dynamically executes a method in the provider associated with a component resource.
	Call(tok tokens.ModuleMember, args resource.PropertyMap, info CallInfo,
		options CallOptions) (CallResult, error)

	// GetPluginInfo returns this plugin's information.
	GetPluginInfo() (workspace.PluginInfo, error)

	// SignalCancellation asks all resource providers to gracefully shut down and abort any ongoing
	// operations. Operation aborted in this way will return an error (e.g., `Update` and `Create`
	// will either a creation error or an initialization error. SignalCancellation is advisory and
	// non-blocking; it is up to the host to decide how long to wait after SignalCancellation is
	// called before (e.g.) hard-closing any gRPC connection.
	SignalCancellation() error
}

type GrpcProvider interface {
	Provider

	// Attach triggers an attach for a currently running provider to the engine
	// TODO It would be nice if this was a HostClient rather than the string address but due to dependency
	// ordering we don't have access to declare that here.
	Attach(address string) error
}

// CheckFailure indicates that a call to check failed; it contains the property and reason for the failure.
type CheckFailure struct {
	Property resource.PropertyKey // the property that failed checking.
	Reason   string               // the reason the property failed to check.
}

// ErrNotYetImplemented may be returned from a provider for optional methods that are not yet implemented.
var ErrNotYetImplemented = errors.New("NYI")

// DiffChanges represents the kind of changes detected by a diff operation.
type DiffChanges int

const (
	// DiffUnknown indicates the provider didn't offer information about the changes (legacy behavior).
	DiffUnknown DiffChanges = 0
	// DiffNone indicates the provider performed a diff and concluded that no update is needed.
	DiffNone DiffChanges = 1
	// DiffSome indicates the provider performed a diff and concluded that an update or replacement is needed.
	DiffSome DiffChanges = 2
)

// DiffKind represents the kind of diff that applies to a particular property.
type DiffKind int

func (d DiffKind) String() string {
	switch d {
	case DiffAdd:
		return "add"
	case DiffAddReplace:
		return "add-replace"
	case DiffDelete:
		return "delete"
	case DiffDeleteReplace:
		return "delete-replace"
	case DiffUpdate:
		return "update"
	case DiffUpdateReplace:
		return "update-replace"
	default:
		contract.Failf("Unknown diff kind %v", int(d))
		return ""
	}
}

func (d DiffKind) IsReplace() bool {
	switch d {
	case DiffAddReplace, DiffDeleteReplace, DiffUpdateReplace:
		return true
	default:
		return false
	}
}

// AsReplace converts a DiffKind into the equivalent replacement if it not already
// a replacement.
func (d DiffKind) AsReplace() DiffKind {
	switch d {
	case DiffAdd:
		return DiffAddReplace
	case DiffAddReplace:
		return DiffAddReplace
	case DiffDelete:
		return DiffDeleteReplace
	case DiffDeleteReplace:
		return DiffDeleteReplace
	case DiffUpdate:
		return DiffUpdateReplace
	case DiffUpdateReplace:
		return DiffUpdateReplace
	default:
		contract.Failf("Unknown diff kind %v", int(d))
		return DiffUpdateReplace
	}
}

const (
	// DiffAdd indicates that the property was added.
	DiffAdd DiffKind = 0
	// DiffAddReplace indicates that the property was added and requires that the resource be replaced.
	DiffAddReplace DiffKind = 1
	// DiffDelete indicates that the property was deleted.
	DiffDelete DiffKind = 2
	// DiffDeleteReplace indicates that the property was added and requires that the resource be replaced.
	DiffDeleteReplace DiffKind = 3
	// DiffUpdate indicates that the property was updated.
	DiffUpdate DiffKind = 4
	// DiffUpdateReplace indicates that the property was updated and requires that the resource be replaced.
	DiffUpdateReplace DiffKind = 5
)

// PropertyDiff records the difference between a single property's old and new values.
type PropertyDiff struct {
	Kind      DiffKind // The kind of diff.
	InputDiff bool     // True if this is a diff between old and new inputs rather than old state and new inputs.
}

// ToReplace converts the kind of a PropertyDiff into the equivalent replacement if it not already
// a replacement.
func (p PropertyDiff) ToReplace() PropertyDiff {
	return PropertyDiff{
		InputDiff: p.InputDiff,
		Kind:      p.Kind.AsReplace(),
	}
}

// DiffResult indicates whether an operation should replace or update an existing resource.
type DiffResult struct {
	Changes             DiffChanges             // true if this diff represents a changed resource.
	ReplaceKeys         []resource.PropertyKey  // an optional list of replacement keys.
	StableKeys          []resource.PropertyKey  // an optional list of property keys that are stable.
	ChangedKeys         []resource.PropertyKey  // an optional list of keys that changed.
	DetailedDiff        map[string]PropertyDiff // an optional structured diff
	DeleteBeforeReplace bool                    // if true, this resource must be deleted before recreating it.
}

// Computes the detailed diff of Updated, Added and Deleted keys.
func NewDetailedDiffFromObjectDiff(diff *resource.ObjectDiff) map[string]PropertyDiff {
	if diff == nil {
		return map[string]PropertyDiff{}
	}
	out := map[string]PropertyDiff{}
	objectDiffToDetailedDiff("", diff, out)
	return out
}

func objectDiffToDetailedDiff(prefix string, diff *resource.ObjectDiff, acc map[string]PropertyDiff) {

	getPrefix := func(k resource.PropertyKey) string {
		if prefix == "" {
			return string(k)
		}
		return fmt.Sprintf("%s.%s", prefix, string(k))
	}

	for k, vd := range diff.Updates {
		nestedPrefix := getPrefix(k)
		valueDiffToDetailedDiff(nestedPrefix, vd, acc)
	}

	for k := range diff.Adds {
		nestedPrefix := getPrefix(k)
		acc[nestedPrefix] = PropertyDiff{Kind: DiffAdd}
	}

	for k := range diff.Deletes {
		nestedPrefix := getPrefix(k)
		acc[nestedPrefix] = PropertyDiff{Kind: DiffDelete}
	}
}

func arrayDiffToDetailedDiff(prefix string, d *resource.ArrayDiff, acc map[string]PropertyDiff) {
	nestedPrefix := func(i int) string { return fmt.Sprintf("%s[%d]", prefix, i) }
	for i, vd := range d.Updates {
		valueDiffToDetailedDiff(nestedPrefix(i), vd, acc)
	}
	for i := range d.Adds {
		acc[nestedPrefix(i)] = PropertyDiff{Kind: DiffAdd}
	}
	for i := range d.Deletes {
		acc[nestedPrefix(i)] = PropertyDiff{Kind: DiffDelete}
	}

}

func valueDiffToDetailedDiff(prefix string, vd resource.ValueDiff, acc map[string]PropertyDiff) {
	if vd.Object != nil {
		objectDiffToDetailedDiff(prefix, vd.Object, acc)
	} else if vd.Array != nil {
		arrayDiffToDetailedDiff(prefix, vd.Array, acc)
	} else {
		switch {
		case vd.Old.V == nil && vd.New.V != nil:
			acc[prefix] = PropertyDiff{Kind: DiffAdd}
		case vd.Old.V != nil && vd.New.V == nil:
			acc[prefix] = PropertyDiff{Kind: DiffDelete}
		default:
			acc[prefix] = PropertyDiff{Kind: DiffUpdate}
		}
	}
}

// Replace returns true if this diff represents a replacement.
func (r DiffResult) Replace() bool {
	for _, v := range r.DetailedDiff {
		if v.Kind.IsReplace() {
			return true
		}
	}
	return len(r.ReplaceKeys) > 0
}

// DiffUnavailableError may be returned by a provider if the provider is unable to diff a resource.
type DiffUnavailableError struct {
	reason string
}

// DiffUnavailable creates a new DiffUnavailableError with the given message.
func DiffUnavailable(reason string) DiffUnavailableError {
	return DiffUnavailableError{reason: reason}
}

// Error returns the error message for this DiffUnavailableError.
func (e DiffUnavailableError) Error() string {
	return e.reason
}

// ReadResult is the result of a call to Read.
type ReadResult struct {
	// This is the ID for the resource. This ID will always be populated and will ensure we get the most up-to-date
	// resource ID.
	ID resource.ID
	// Inputs contains the new inputs for the resource, if any. If this field is nil, the provider does not support
	// returning inputs from a call to Read and the old inputs (if any) should be preserved.
	Inputs resource.PropertyMap
	// Outputs contains the new outputs/state for the resource, if any. If this field is nil, the resource does not
	// exist.
	Outputs resource.PropertyMap
}

// ConstructInfo contains all of the information required to register resources as part of a call to Construct.
type ConstructInfo struct {
	Project          string                // the project name housing the program being run.
	Stack            string                // the stack name being evaluated.
	Config           map[config.Key]string // the configuration variables to apply before running.
	ConfigSecretKeys []config.Key          // the configuration keys that have secret values.
	DryRun           bool                  // true if we are performing a dry-run (preview).
	Parallel         int                   // the degree of parallelism for resource operations (<=1 for serial).
	MonitorAddress   string                // the RPC address to the host resource monitor.
}

// ConstructOptions captures options for a call to Construct.
type ConstructOptions struct {
	// Aliases is the set of aliases for the component.
	Aliases []resource.Alias
	// Dependencies is the list of resources this component depends on.
	Dependencies []resource.URN
	// Protect is true if the component is protected.
	Protect bool
	// Providers is a map from package name to provider reference.
	Providers map[string]string
	// PropertyDependencies is a map from property name to a list of resources that property depends on.
	PropertyDependencies map[resource.PropertyKey][]resource.URN
}

// ConstructResult is the result of a call to Construct.
type ConstructResult struct {
	// The URN of the constructed component resource.
	URN resource.URN
	// The output properties of the component resource.
	Outputs resource.PropertyMap
	// The resources that each output property depends on.
	OutputDependencies map[resource.PropertyKey][]resource.URN
}

// CallInfo contains all of the information required to register resources as part of a call to Construct.
type CallInfo struct {
	Project        string                // the project name housing the program being run.
	Stack          string                // the stack name being evaluated.
	Config         map[config.Key]string // the configuration variables to apply before running.
	DryRun         bool                  // true if we are performing a dry-run (preview).
	Parallel       int                   // the degree of parallelism for resource operations (<=1 for serial).
	MonitorAddress string                // the RPC address to the host resource monitor.
}

// CallOptions captures options for a call to Call.
type CallOptions struct {
	// ArgDependencies is a map from argument keys to a list of resources that the argument depends on.
	ArgDependencies map[resource.PropertyKey][]resource.URN
}

// CallResult is the result of a call to Call.
type CallResult struct {
	// The returned values, if the call was successful.
	Return resource.PropertyMap
	// A map from return value keys to the dependencies of the return value.
	ReturnDependencies map[resource.PropertyKey][]resource.URN
	// The failures if any arguments didn't pass verification.
	Failures []CheckFailure
}
