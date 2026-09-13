package main

import "fmt"

func validateProfiles(profiles []Profile) error {
	if len(profiles) == 0 || len(profiles) > 256 {
		return fmt.Errorf("profile count must be between 1 and 256")
	}
	names := make(map[string]bool)
	for _, p := range profiles {
		if p.TLSInsecure {
			return fmt.Errorf("insecure TLS profiles are not supported")
		}
		if p.Name != "" && names[p.Name] {
			return fmt.Errorf("duplicate profile name")
		}
		names[p.Name] = true
		outbound, address, port, _, err := buildOutbound(p.Link)
		if err != nil || address == "" || port == "" {
			return fmt.Errorf("invalid or unsupported proxy profile")
		}
		if outbound.Protocol == "vless" && (outbound.StreamSetting == nil || (outbound.StreamSetting.Security != "tls" && outbound.StreamSetting.Security != "reality")) {
			return fmt.Errorf("VLESS profiles require TLS or REALITY")
		}
		if _, err := outbound.Build(); err != nil {
			return fmt.Errorf("invalid or unsupported outbound settings")
		}
	}
	return nil
}
