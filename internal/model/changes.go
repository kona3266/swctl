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

	for key, n := range new.BGPPeers {
		o, ok := old.BGPPeers[key]
		if !ok {
			out = append(out, ObjectChange{Resource: "bgp-neighbor", Name: key, Action: "create",
				Fields: []FieldChange{{Field: "remote_as", To: n.RemoteAS}}})
			continue
		}
		if o.RemoteAS != n.RemoteAS {
			out = append(out, ObjectChange{Resource: "bgp-neighbor", Name: key, Action: "update",
				Fields: []FieldChange{{Field: "remote_as", From: o.RemoteAS, To: n.RemoteAS}}})
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
