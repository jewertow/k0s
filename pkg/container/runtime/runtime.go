package runtime

type ContainerRuntime interface {
	ListContainers() ([]string, error)
	RemoveContainer(id string) error
	StopContainer(id string) error
}

func NewContainerRuntime(runtimeType string, criSocketPath string) (ContainerRuntime, error) {
	if runtimeType == "docker" {
		return nil, nil
	}
	return &CRIRuntime{criSocketPath}, nil
}
