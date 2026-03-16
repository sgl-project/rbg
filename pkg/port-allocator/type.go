package port_allocator

type PortPolicy string

const (
	// Dynamic Port is only valid for the current Pod
	Dynamic PortPolicy = "Dynamic"
	// Static Port is valid for all Pod replicas in the current role
	Static PortPolicy = "Static"
)

type PortAllocatorConfig struct {
	// Allocations specifies the ports to be allocated
	Allocations []PortAllocation `json:"allocations"`
	// References specifies the ports to be referenced from other pod
	References []PortReference `json:"references"`
}

type PortAllocation struct {
	// Not Empty
	// Name specifies the name of the port
	Name string `json:"name"`
	// Not Empty
	// Env specifies the name of the environment variable to be injected into the container
	Env string `json:"env"`
	// AnnotationKey specifies the key of the annotation to be injected into the Pod
	AnnotationKey string `json:"annotationKey"`
	// Not Empty
	// Default is Dynamic
	// Policy specifies the scope of the port
	Policy PortPolicy `json:"policy"`
}

type PortReference struct {
	// Not Empty
	// Env specifies the name of the environment variable to be injected into the container
	Env string `json:"env"`
	// Not Empty
	// From specifies the name of the port to be referenced
	From string `json:"from"`
}
