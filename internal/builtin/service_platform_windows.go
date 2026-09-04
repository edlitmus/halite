package builtin

// platformServiceProviders adds the service control manager.
//
// It is here rather than in the cross-platform list because it is
// reached through an API rather than by running a binary, so it does not
// compile anywhere else. SPEC 15.2 names `windows` among the providers
// the virtual `service` module has.
func platformServiceProviders() []serviceProvider { return []serviceProvider{windowsProvider{}} }
