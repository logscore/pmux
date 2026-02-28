package cmd

import "github.com/logscore/roxy/pkg/config"

// RunService converts a ServiceConfig into RunOptions and calls Run().
func RunService(name string, svc config.ServiceConfig, detach bool) error {
	opts := RunOptions{
		Command:    svc.Cmd,
		Name:       svc.Name,
		StartPort:  svc.Port,
		TLS:        svc.TLS,
		Detach:     detach,
		ListenPort: svc.ListenPort,
	}
	if opts.Name == "" {
		opts.Name = name
	}
	return Run(opts)
}
