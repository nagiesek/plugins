package windows

import (
	"sort"
	"github.com/containernetworking/cni/pkg/types"
	"strings"
	"os"
	"regexp"
)

type dnsSuffixSlice []string

func (d dnsSuffixSlice) Len() int {
	return len(d)
}

func (d dnsSuffixSlice) Less(i, j int) bool {
	iLen := len(d[i])
	jLen := len(d[j])

	if iLen == jLen {
		return d[i] > d[j]
	}

	return iLen > jLen
}

func (d dnsSuffixSlice) Swap(i, j int) {
	d[i], d[j] = d[j], d[i]
}

func distinctSlice(cols ...[]string) []string {
	ret := make([]string, 0, 8)

	if len(cols) != 0 {
		m := make(map[string]bool, 8)
		for _, col := range cols {
			if len(col) != 0 {
				for _, c := range col {
					if len(c) != 0 {
						m[c] = true
					}
				}
			}
		}

		for r := range m {
			ret = append(ret, r)
		}
	}

	return ret
}

type cniArgs struct {
	types.CommonArgs

	K8S_POD_NAMESPACE types.UnmarshallableString `json:"K8S_POD_NAMESPACE,omitempty"`
	// K8S_POD_NAME             types.UnmarshallableString `json:"K8S_POD_NAME,omitempty"`
	// K8S_POD_INFRA_CONTAINER_ID types.UnmarshallableString `json:"K8S_POD_INFRA_CONTAINER_ID,omitempty"`
}

func parseCniArgs(args string) *cniArgs {
	newCniArgs := cniArgs{}
	types.LoadArgs(args, &newCniArgs)
	return &newCniArgs
}

var (
	k8sCommonDNSSuffixRegexp = regexp.MustCompile(`^(svc\.|pod\.).*`)
)

func PatchKubernetesDNS(args string, dnsSearch []string) []string {
	cniArgs := parseCniArgs(args)
	appendDNSSuffixWithNamespace := strings.EqualFold(os.Getenv("KUBE_APPEND_DNS_SUFFIX_WITH_NS"), "true")

	if len(cniArgs.K8S_POD_NAMESPACE) != 0 {
		k8sPodNamespace := string(cniArgs.K8S_POD_NAMESPACE)

		for idx, suffix := range dnsSearch {
			if k8sCommonDNSSuffixRegexp.MatchString(suffix) {
				if appendDNSSuffixWithNamespace {
					dnsSearch = append(dnsSearch, k8sPodNamespace+"."+suffix)
				} else {
					dnsSearch[idx] = k8sPodNamespace + "." + suffix
				}
			}
		}
	}

	dnsSearch = distinctSlice(dnsSearch)
	sort.Sort(dnsSuffixSlice(dnsSearch))
	return dnsSearch
}
