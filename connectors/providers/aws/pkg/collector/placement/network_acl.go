package placement

import (
	"context"
	"fmt"
	"net/netip"
	"sort"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

type ACLRule struct {
	Number   int32
	Egress   bool
	Allow    bool
	Protocol string
	CIDR     string
	From, To int32
}

type networkACLReader interface {
	DescribeNetworkAcls(context.Context, *ec2.DescribeNetworkAclsInput, ...func(*ec2.Options)) (*ec2.DescribeNetworkAclsOutput, error)
}

type vpcDNSReader interface {
	DescribeVpcAttribute(context.Context, *ec2.DescribeVpcAttributeInput, ...func(*ec2.Options)) (*ec2.DescribeVpcAttributeOutput, error)
}

func discoverNetworkACLs(ctx context.Context, api networkACLReader, subnets []Subnet) error {
	bySubnet := map[string][]ACLRule{}
	pager := ec2.NewDescribeNetworkAclsPaginator(api, &ec2.DescribeNetworkAclsInput{})
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("describe network ACLs: %w", err)
		}
		for _, acl := range page.NetworkAcls {
			var rules []ACLRule
			for _, entry := range acl.Entries {
				rule := ACLRule{Number: aws.ToInt32(entry.RuleNumber), Egress: aws.ToBool(entry.Egress), Allow: entry.RuleAction == ec2types.RuleActionAllow, Protocol: aws.ToString(entry.Protocol), CIDR: aws.ToString(entry.CidrBlock), From: 0, To: 65535}
				if entry.PortRange != nil {
					rule.From, rule.To = aws.ToInt32(entry.PortRange.From), aws.ToInt32(entry.PortRange.To)
				}
				if rule.CIDR != "" {
					rules = append(rules, rule)
				}
			}
			sort.Slice(rules, func(i, j int) bool { return rules[i].Number < rules[j].Number })
			for _, association := range acl.Associations {
				bySubnet[aws.ToString(association.SubnetId)] = rules
			}
		}
	}
	for idx := range subnets {
		subnets[idx].ACLKnown = true
		subnets[idx].ACLRules = bySubnet[subnets[idx].ID]
	}
	return nil
}

// A partial allow cannot prove admission for every address and port. An earlier
// overlapping deny wins, matching AWS's ordered, stateless ACL evaluation.
func aclAllows(subnet Subnet, remote string, egress bool, from, to int32) bool {
	if !subnet.ACLKnown {
		return true
	}
	prefix, err := netip.ParsePrefix(remote)
	if err != nil {
		return false
	}
	for _, rule := range subnet.ACLRules {
		if rule.Egress != egress || (rule.Protocol != "-1" && rule.Protocol != "6") || rule.To < from || rule.From > to {
			continue
		}
		network, err := netip.ParsePrefix(rule.CIDR)
		if err != nil || !network.Overlaps(prefix) {
			continue
		}
		if !rule.Allow {
			return false
		}
		if network.Bits() <= prefix.Bits() && rule.From <= from && rule.To >= to {
			return true
		}
	}
	return false
}

func (t Topology) aclPathAllowed(hostIDs, sourceIDs []string, ports []int32) bool {
	for _, hostID := range hostIDs {
		for _, sourceID := range sourceIDs {
			host, hok := t.subnets[hostID]
			source, sok := t.subnets[sourceID]
			if !hok || !sok {
				continue
			}
			for _, port := range ports {
				if !aclAllows(source, host.CIDRBlock, true, port, port) || !aclAllows(host, source.CIDRBlock, false, port, port) || !aclAllows(host, source.CIDRBlock, true, 1024, 65535) || !aclAllows(source, host.CIDRBlock, false, 1024, 65535) {
					return false
				}
			}
		}
	}
	return true
}

func (t Topology) workloadEgressAllowed(sourceIDs []string, collectorID string, destinationCIDRs []string, port int32) bool {
	known := false
	for _, id := range sourceIDs {
		group := t.securityGroups[id]
		if !group.EgressKnown {
			continue
		}
		known = true
		for _, rule := range group.Egress {
			if !securityGroupRuleAllowsPort(rule, port) {
				continue
			}
			for _, target := range rule.SourceSecurityGroupIDs {
				if target == collectorID {
					return true
				}
			}
			covered := len(destinationCIDRs) > 0
			for _, destination := range destinationCIDRs {
				found := false
				for _, allowed := range rule.SourceCIDRs {
					found = found || cidrContains(allowed, destination)
				}
				covered = covered && found
			}
			if covered {
				return true
			}
		}
	}
	return !known
}
