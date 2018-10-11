
// Copyright 2017 CNI authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package hns

import (
	"fmt"
	"net"
	"strings"

	"github.com/Microsoft/hcsshim"
	"github.com/Microsoft/hcsshim/hcn"
	"github.com/containernetworking/cni/pkg/types/current"
	"github.com/juju/errors"
)

const (
	pauseContainerNetNS = "none"
)

type EndpointInfo struct {
	EndpointName     string
	DnsSuffix        string
	NetworkName      string
	NetworkId        string
	Gateway          net.IP
	IpAddress        net.IP
	Nameservers      []string
}

// GetSandboxContainerID returns the sandbox ID of this pod
func GetSandboxContainerID(containerID string, netNs string) string {
	if len(netNs) != 0 && netNs != pauseContainerNetNS {
		splits := strings.SplitN(netNs, ":", 2)
		if len(splits) == 2 {
			containerID = splits[1]
		}
	}

	return containerID
}

func GenerateHnsEndpoint(epInfo *EndpointInfo, n *NetConf) *hcsshim.HNSEndpoint {
	// run the IPAM plugin and get back the config to apply
	hnsEndpoint, err := hcsshim.GetHNSEndpointByName(epInfo.EndpointName)
	if hnsEndpoint != nil && err != nil {
		if hnsEndpoint.VirtualNetwork != epInfo.NetworkId {
			_, err = hnsEndpoint.Delete()
			if err != nil {
				errors.Annotatef(err, "[win-cni] Failed to delete endpoint %v, err:%v", epInfo.EndpointName, err)
			}
		} else {
			errors.Annotatef(err, "[win-cni] Found existing endpoint %v", epInfo.EndpointName)
		}
	} else {
		
		hnsEndpoint = &hcsshim.HNSEndpoint{
			Name:           epInfo.EndpointName,
			VirtualNetwork: epInfo.NetworkId,
			DNSServerList:  strings.Join(epInfo.Nameservers, ","),
			DNSSuffix:      epInfo.DnsSuffix,
			GatewayAddress: epInfo.Gateway.String(),
			IPAddress:      epInfo.IpAddress,
			Policies:       n.MarshalPolicies(),
		}

		errors.Annotatef(err, "Adding Hns Endpoint %v", hnsEndpoint)
	}
	return hnsEndpoint
}

func GenerateHcnEndpoint(epInfo *EndpointInfo, n *NetConf) *hcn.HostComputeEndpoint {
	// run the IPAM plugin and get back the config to apply
	hcnEndpoint, err := hcn.GetEndpointByName(epInfo.EndpointName)
	if hcnEndpoint != nil && err != nil {
		if hcnEndpoint.HostComputeNetwork != epInfo.NetworkId {
			_, err = hcnEndpoint.Delete()
			if err != nil {
				errors.Annotatef(err, "[win-cni] Failed to delete endpoint %v, err:%v", epInfo.EndpointName, err)
			}
		} else {
			errors.Annotatef(err, "[win-cni] Found existing endpoint %v", epInfo.EndpointName)
		}
	} else {
		
		routes := []hcn.Route{
			hcn.Route{
			NextHop: epInfo.Gateway.String(),
			DestinationPrefix: 		func() string {
			destinationPrefix := "0.0.0.0/0"
				if ipv6 := epInfo.Gateway.To4(); ipv6 == nil {
					destinationPrefix = "::/0"
				}
				return destinationPrefix
			}(),   
			},
		}
			
		hcnDns := hcn.Dns{
			Suffix:     epInfo.DnsSuffix,
			ServerList: epInfo.Nameservers,
		}

		hcnIpConfig := hcn.IpConfig{
			IpAddress:  epInfo.IpAddress.String(),
		}
		ipConfigs := []hcn.IpConfig{hcnIpConfig}
		
		hcnEndpoint = &hcn.HostComputeEndpoint{
			SchemaVersion: hcn.Version{Major: 2,},
			Name:                epInfo.EndpointName,
			HostComputeNetwork:  epInfo.NetworkId,
		    Dns:                 hcnDns,
		    Routes:              routes,
			IpConfigurations:    ipConfigs,
			Policies:            func() []hcn.EndpointPolicy {
				if n.HcnPolicyArgs == nil {
					n.HcnPolicyArgs = []hcn.EndpointPolicy{}
				}
				return n.HcnPolicyArgs
			}(),
		}

		errors.Annotatef(err, "Adding Hns Endpoint %v", hcnEndpoint)
	}
	return hcnEndpoint
}


// ConstructEndpointName constructs enpointId which is used to identify an endpoint from HNS
// There is a special consideration for netNs name here, which is required for Windows Server 1709
// containerID is the Id of the container on which the endpoint is worked on
func ConstructEndpointName(containerID string, netNs string, networkName string) string {
	return GetSandboxContainerID(containerID, netNs) + "_" + networkName
}

// DeprovisionEndpoint removes an endpoint from the container by sending a Detach request to HNS
// For shared endpoint, ContainerDetach is used
// for removing the endpoint completely, HotDetachEndpoint is used
func DeprovisionEndpoint(epName string, netns string, containerID string) error {
	if len(netns) == 0 {
		return nil
	}

	hnsEndpoint, err := hcsshim.GetHNSEndpointByName(epName)
	if err != nil {
		return errors.Annotatef(err, "failed to find HNSEndpoint %s", epName)
	}

	if netns != pauseContainerNetNS {
		// Shared endpoint removal. Do not remove the endpoint.
		hnsEndpoint.ContainerDetach(containerID)
		return nil
	}

	// Do not consider this as failure, else this would leak endpoints
	hcsshim.HotDetachEndpoint(containerID, hnsEndpoint.Id)

	// Do not return error
	hnsEndpoint.Delete()

	return nil
}

type EndpointMakerFunc func() (*hcsshim.HNSEndpoint, error)

// ProvisionEndpoint provisions an endpoint to a container specified by containerID.
// If an endpoint already exists, the endpoint is reused.
// This call is idempotent
func ProvisionEndpoint(epName string, expectedNetworkId string, containerID string, makeEndpoint EndpointMakerFunc) (*hcsshim.HNSEndpoint, error) {
	// check if endpoint already exists
	createEndpoint := true
	hnsEndpoint, err := hcsshim.GetHNSEndpointByName(epName)
	if hnsEndpoint != nil && hnsEndpoint.VirtualNetwork == expectedNetworkId {
		createEndpoint = false
	}

	if createEndpoint {
		if hnsEndpoint != nil {
			if _, err = hnsEndpoint.Delete(); err != nil {
				return nil, errors.Annotate(err, "failed to delete the stale HNSEndpoint")
			}
		}

		if hnsEndpoint, err = makeEndpoint(); err != nil {
			return nil, errors.Annotate(err, "failed to make a new HNSEndpoint")
		}

		if hnsEndpoint, err = hnsEndpoint.Create(); err != nil {
			return nil, errors.Annotate(err, "failed to create the new HNSEndpoint")
		}

	}

	// hot attach
	if err := hcsshim.HotAttachEndpoint(containerID, hnsEndpoint.Id); err != nil {
		if hcsshim.ErrComputeSystemDoesNotExist == err {
			return hnsEndpoint, nil
		}

		return nil, err
	}

	return hnsEndpoint, nil
}

type HcnEndpointMakerFunc func() (*hcn.HostComputeEndpoint, error)

func AddHcnEndpoint(epName string, expectedNetworkId string, namespace string,
	makeEndpoint HcnEndpointMakerFunc) (*hcn.HostComputeEndpoint, error) {
	// check if endpoint already exists
	createEndpoint := true
	hcnEndpoint, err := hcn.GetEndpointByName(epName)
	if hcnEndpoint != nil && hcnEndpoint.HostComputeNetwork == expectedNetworkId {
		createEndpoint = false
	}
	if createEndpoint {
		if hcnEndpoint != nil {
			if _, err = hcnEndpoint.Delete(); err != nil {
				return nil, errors.Annotate(err, "failed to delete the stale HNSEndpoint")
			}
		}

		if hcnEndpoint, err = makeEndpoint(); err != nil {
			return nil, errors.Annotate(err, "failed to make a new HNSEndpoint")
		}
		
		if hcnEndpoint, err = hcnEndpoint.Create(); err != nil {
			return nil, errors.Annotate(err, "failed to create the new HNSEndpoint")
		}

	}
	hcn.AddNamespaceEndpoint(hcnEndpoint.Id, namespace)
	return hcnEndpoint, nil

}

// ConstructResult constructs the CNI result for the endpoint
func ConstructResult(hnsNetwork *hcsshim.HNSNetwork, hnsEndpoint *hcsshim.HNSEndpoint) (*current.Result, error) {
	resultInterface := &current.Interface{
		Name: hnsEndpoint.Name,
		Mac:  hnsEndpoint.MacAddress,
	}
	_, ipSubnet, err := net.ParseCIDR(hnsNetwork.Subnets[0].AddressPrefix)
	if err != nil {
		return nil, errors.Annotatef(err, "failed to parse CIDR from %s", hnsNetwork.Subnets[0].AddressPrefix)
	}

	var ipVersion string
	if ipv4 := hnsEndpoint.IPAddress.To4(); ipv4 != nil {
		ipVersion = "4"
	} else if ipv6 := hnsEndpoint.IPAddress.To16(); ipv6 != nil {
		ipVersion = "6"
	} else {
		return nil, fmt.Errorf("IPAddress of HNSEndpoint %s isn't a valid ipv4 or ipv6 Address", hnsEndpoint.Name)
	}

	resultIPConfig := &current.IPConfig{
		Version: ipVersion,
		Address: net.IPNet{
			IP:   hnsEndpoint.IPAddress,
			Mask: ipSubnet.Mask},
		Gateway: net.ParseIP(hnsEndpoint.GatewayAddress),
	}
	result := &current.Result{}
	result.Interfaces = []*current.Interface{resultInterface}
	result.IPs = []*current.IPConfig{resultIPConfig}

	return result, nil
}

// This verison follows the v2 workflow of removing the endpoint from the namespace and deleting it 
func RemoveHcnEndpoint(epName string) error {
	hcnEndpoint, err := hcn.GetEndpointByName(epName)
	if err != nil {
		fmt.Errorf("[win-cni] Failed to find endpoint %v, err:%v", epName, err)
		return err
	}
	if hcnEndpoint != nil {
		hcnEndpoint, err = hcnEndpoint.Delete()
		if err != nil {
			fmt.Errorf("[win-cni] Failed to delete endpoint %v, err:%v", epName, err)
		}
	}
	return nil
}

// Add and endpoint to the namespace
func AddNamespaceEndpoint(hcnEndpointId string, namespace string) error {
	fmt.Errorf("[win-cni] attaching Endpoint: %v to namespace: %v", hcnEndpointId, namespace)
	hcn.AddNamespaceEndpoint(namespace, hcnEndpointId)
	return nil
}

func ConstructHcnResult(hcnNetwork *hcn.HostComputeNetwork, hcnEndpoint *hcn.HostComputeEndpoint) (*current.Result, error) {
	resultInterface := &current.Interface{
		Name: hcnEndpoint.Name,
		Mac:  hcnEndpoint.MacAddress,
	}
	_, ipSubnet, err := net.ParseCIDR(hcnNetwork.Ipams[0].Subnets[0].IpAddressPrefix)
	if err != nil {
		return nil, err
	}

	var ipVersion string
	ipAddress := net.ParseIP(hcnEndpoint.IpConfigurations[0].IpAddress)
	if ipv4 := ipAddress.To4(); ipv4 != nil {
		ipVersion = "4"
	} else if ipv6 := ipAddress.To16(); ipv6 != nil {
		ipVersion = "6"
	} else {
		return nil, fmt.Errorf("[win-cni] The IPAddress of hnsEndpoint isn't a valid ipv4 or ipv6 Address.")
	}

	resultIPConfig := &current.IPConfig{
		Version: ipVersion,
		Address: net.IPNet{
			IP:   ipAddress,
			Mask: ipSubnet.Mask},
		Gateway: net.ParseIP(hcnEndpoint.Routes[0].NextHop),
	}
	result := &current.Result{}
	result.Interfaces = []*current.Interface{resultInterface}
	result.IPs = []*current.IPConfig{resultIPConfig}

	return result, nil
}

