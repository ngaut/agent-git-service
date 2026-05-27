package auth

// Identity is a trusted host-provided identity for embedded deployments.
type Identity struct {
	Provider  string
	Subject   string
	Login     string
	Name      string
	Email     string
	Groups    []string
	SiteAdmin bool
}
