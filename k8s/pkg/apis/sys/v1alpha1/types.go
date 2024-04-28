package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// FSWatcher is the Schema for the Juice FS notification watcher
type FSWatcher struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FSWatcherSpec   `json:"spec,omitempty"`
	Status FSWatcherStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type FSWatcherList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []FSWatcher `json:"items"`
}

type FSWatcherStatus struct {
	// the state of the application: draft, submitted, passed, rejected, suspended, active
	State      string       `json:"state"`
	UpdateTime *metav1.Time `json:"updateTime,omitempty"`
	StatusTime *metav1.Time `json:"statusTime,omitempty"`
}

type FSWatcherSpec struct {
	Description string   `json:"description,omitempty"`
	Pod         string   `json:"pod,omitempty"`
	Container   string   `json:"container,omitempty"`
	Paths       []string `json:"paths,omitempty"`
}
