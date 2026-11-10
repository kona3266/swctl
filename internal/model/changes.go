package model

import (
	"sort"
	"strconv"
)

// FieldDiff 描述"这一步变更了什么"（快路径渲染与审计用）。
// 由旧权威模型与新权威模型的同名对象比较得出。
type FieldChange struct {
	Field string `json:"field"`
	From  string `json:"from,omitempty"`
	To    string `json:"to,omitempty"`
}

// ObjectChange 是一个对象的变更描述（快路径审计摘要）。
type ObjectChange struct {
	Resource string        `json:"resource"` // vrf | vlan
	Name     string        `json:"name"`
	Action   string        `json:"action"` // create | update | delete
	Fields   []FieldChange `json:"fields,omitempty"`
}

// DescribeChanges 比较旧的权威模型与新的权威模型，输出本次变更摘要。
func DescribeChanges(old, new *DeviceConfig) []ObjectChange {
	var out []ObjectChange

	for name, n := range new.VRFs {
		o, ok := old.VRFs[name]
		if !ok {
			out = append(out, ObjectChange{Resource: "vrf", Name: name, Action: "create",
				Fields: []FieldChange{{Field: "rd", To: n.RD}}})
			continue
		}
		if o.RD != n.RD {
			out = append(out, ObjectChange{Resource: "vrf", Name: name, Action: "update",
				Fields: []FieldChange{{Field: "rd", From: o.RD, To: n.RD}}})
		}
	}
	for name := range old.VRFs {
		if _, ok := new.VRFs[name]; !ok {
			out = append(out, ObjectChange{Resource: "vrf", Name: name, Action: "delete"})
		}
	}

	for id, n := range new.VLANs {
		o, ok := old.VLANs[id]
		if !ok {
			out = append(out, ObjectChange{Resource: "vlan", Name: strconv.Itoa(id), Action: "create",
				Fields: []FieldChange{{Field: "name", To: n.Name}}})
			continue
		}
		if o.Name != n.Name {
			out = append(out, ObjectChange{Resource: "vlan", Name: strconv.Itoa(id), Action: "update",
				Fields: []FieldChange{{Field: "name", From: o.Name, To: n.Name}}})
		}
	}
	for id := range old.VLANs {
		if _, ok := new.VLANs[id]; !ok {
			out = append(out, ObjectChange{Resource: "vlan", Name: strconv.Itoa(id), Action: "delete"})
		}
	}

	for id, n := range new.SVIs {
		o, ok := old.SVIs[id]
		if !ok {
			out = append(out, ObjectChange{Resource: "svi", Name: strconv.Itoa(id), Action: "create",
				Fields: []FieldChange{{Field: "ip", To: n.IP}, {Field: "vrf", To: n.VRF}}})
			continue
		}
		var fields []FieldChange
		if o.IP != n.IP {
			fields = append(fields, FieldChange{Field: "ip", From: o.IP, To: n.IP})
		}
		if o.VRF != n.VRF {
			fields = append(fields, FieldChange{Field: "vrf", From: o.VRF, To: n.VRF})
		}
		if len(fields) > 0 {
			out = append(out, ObjectChange{Resource: "svi", Name: strconv.Itoa(id), Action: "update", Fields: fields})
		}
	}
	for id := range old.SVIs {
		if _, ok := new.SVIs[id]; !ok {
			out = append(out, ObjectChange{Resource: "svi", Name: strconv.Itoa(id), Action: "delete"})
		}
	}

	for name, n := range new.Ports {
		o, ok := old.Ports[name]
		if !ok {
			out = append(out, ObjectChange{Resource: "port", Name: name, Action: "create",
				Fields: []FieldChange{{Field: "mode", To: n.Mode.String()}}})
			continue
		}
		if o.Mode != n.Mode {
			out = append(out, ObjectChange{Resource: "port", Name: name, Action: "update",
				Fields: []FieldChange{{Field: "mode", From: o.Mode.String(), To: n.Mode.String()}}})
		}
	}

	for key, n := range new.SubInterfaces {
		o, ok := old.SubInterfaces[key]
		if !ok {
			fields := []FieldChange{{Field: "ip", To: n.IP}}
			if n.VRF != "" {
				fields = append(fields, FieldChange{Field: "vrf", To: n.VRF})
			}
			out = append(out, ObjectChange{Resource: "sub-interface", Name: key, Action: "create",
				Fields: fields})
			continue
		}
		var fields []FieldChange
		if o.IP != n.IP {
			fields = append(fields, FieldChange{Field: "ip", From: o.IP, To: n.IP})
		}
		if o.VRF != n.VRF {
			fields = append(fields, FieldChange{Field: "vrf", From: o.VRF, To: n.VRF})
		}
		if len(fields) > 0 {
			out = append(out, ObjectChange{Resource: "sub-interface", Name: key, Action: "update", Fields: fields})
		}
	}
	for key := range old.SubInterfaces {
		if _, ok := new.SubInterfaces[key]; !ok {
			out = append(out, ObjectChange{Resource: "sub-interface", Name: key, Action: "delete"})
		}
	}

	for key, n := range new.BGPPeers {
		o, ok := old.BGPPeers[key]
		if !ok {
			out = append(out, ObjectChange{Resource: "bgp-neighbor", Name: key, Action: "create",
				Fields: peerCreateFields(n)})
			continue
		}
		if fields := peerChangedFields(o, n); len(fields) > 0 {
			out = append(out, ObjectChange{Resource: "bgp-neighbor", Name: key, Action: "update",
				Fields: fields})
		}
	}
	for key := range old.BGPPeers {
		if _, ok := new.BGPPeers[key]; !ok {
			out = append(out, ObjectChange{Resource: "bgp-neighbor", Name: key, Action: "delete"})
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Resource != out[j].Resource {
			return out[i].Resource < out[j].Resource
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// peerCreateFields 汇总新建邻居时显式设置的非默认参数。
func peerCreateFields(n *BGPPeer) []FieldChange {
	fields := []FieldChange{{Field: "remote_as", To: n.RemoteAS}}
	if n.SourceIP != "" {
		fields = append(fields, FieldChange{Field: "source_ip", To: n.SourceIP})
	}
	if n.Password != "" {
		fields = append(fields, FieldChange{Field: "password", To: MaskedSecret})
	}
	if n.BFD {
		fields = append(fields, FieldChange{Field: "bfd", To: strconv.FormatBool(true)})
	}
	if n.BFDMinRxMS != 0 {
		fields = append(fields, FieldChange{Field: "bfd_min_rx_ms", To: strconv.Itoa(n.BFDMinRxMS)})
	}
	if n.BFDMinTxMS != 0 {
		fields = append(fields, FieldChange{Field: "bfd_min_tx_ms", To: strconv.Itoa(n.BFDMinTxMS)})
	}
	if n.BFDMultiplier != 0 {
		fields = append(fields, FieldChange{Field: "bfd_multiplier", To: strconv.Itoa(n.BFDMultiplier)})
	}
	if n.MaxPrefix != 0 {
		fields = append(fields, FieldChange{Field: "max_prefix", To: strconv.Itoa(n.MaxPrefix)})
	}
	if n.MaxPrefixWarningOnly {
		fields = append(fields, FieldChange{Field: "max_prefix_warning_only", To: strconv.FormatBool(true)})
	}
	if n.RouteReflectorClient {
		fields = append(fields, FieldChange{Field: "route_reflector_client", To: strconv.FormatBool(true)})
	}
	return fields
}

// peerChangedFields 逐字段比较邻居，密码只记“设置/变更/清除”，不输出明文。
func peerChangedFields(o, n *BGPPeer) []FieldChange {
	var fields []FieldChange
	if o.RemoteAS != n.RemoteAS {
		fields = append(fields, FieldChange{Field: "remote_as", From: o.RemoteAS, To: n.RemoteAS})
	}
	if o.SourceIP != n.SourceIP {
		fields = append(fields, FieldChange{Field: "source_ip", From: o.SourceIP, To: n.SourceIP})
	}
	if o.Password != n.Password {
		f := FieldChange{Field: "password"}
		if o.Password != "" {
			f.From = MaskedSecret
		}
		if n.Password != "" {
			f.To = MaskedSecret
		}
		fields = append(fields, f)
	}
	if o.BFD != n.BFD {
		fields = append(fields, boolFieldChange("bfd", o.BFD, n.BFD))
	}
	if o.BFDMinRxMS != n.BFDMinRxMS {
		fields = append(fields, intFieldChange("bfd_min_rx_ms", o.BFDMinRxMS, n.BFDMinRxMS))
	}
	if o.BFDMinTxMS != n.BFDMinTxMS {
		fields = append(fields, intFieldChange("bfd_min_tx_ms", o.BFDMinTxMS, n.BFDMinTxMS))
	}
	if o.BFDMultiplier != n.BFDMultiplier {
		fields = append(fields, intFieldChange("bfd_multiplier", o.BFDMultiplier, n.BFDMultiplier))
	}
	if o.MaxPrefix != n.MaxPrefix {
		fields = append(fields, intFieldChange("max_prefix", o.MaxPrefix, n.MaxPrefix))
	}
	if o.MaxPrefixWarningOnly != n.MaxPrefixWarningOnly {
		fields = append(fields, boolFieldChange("max_prefix_warning_only", o.MaxPrefixWarningOnly, n.MaxPrefixWarningOnly))
	}
	if o.RouteReflectorClient != n.RouteReflectorClient {
		fields = append(fields, boolFieldChange("route_reflector_client", o.RouteReflectorClient, n.RouteReflectorClient))
	}
	return fields
}

func intFieldChange(name string, from, to int) FieldChange {
	return FieldChange{Field: name, From: strconv.Itoa(from), To: strconv.Itoa(to)}
}

func boolFieldChange(name string, from, to bool) FieldChange {
	return FieldChange{Field: name, From: strconv.FormatBool(from), To: strconv.FormatBool(to)}
}
