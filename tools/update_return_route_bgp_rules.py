#!/usr/bin/env python3

import datetime
import ipaddress
import json
import pathlib
import urllib.request


ROOT = pathlib.Path(__file__).resolve().parents[1]
BASE_RULES = ROOT / "database/tasks/return_route_signatures.json"
OVERRIDES = ROOT / "database/tasks/return_route_bgp_overrides.json"
OUTPUT = ROOT / "database/tasks/return_route_bgp_prefixes.json"
TABLE_URL = "https://bgp.tools/table.jsonl"
USER_AGENT = "Komari-Return-Route (+https://github.com/nuomiiiii/komari)"

# Equal-length BGP prefixes can legitimately have more than one origin ASN.
# Keep the route family that is checked first by the runtime classifier.
GROUP_PRIORITY = [
    "cmin2",
    "cmi",
    "cn2_global",
    "unicom_10099",
    "unicom_9929",
    "cn2_backbone",
    "telecom_163",
    "unicom_4837",
    "cmnet",
]


def collapse(values):
    networks = {ipaddress.ip_network(value, strict=False) for value in values}
    ipv4 = ipaddress.collapse_addresses(network for network in networks if network.version == 4)
    ipv6 = ipaddress.collapse_addresses(network for network in networks if network.version == 6)
    return [str(network) for network in [*ipv4, *ipv6]]


def network_sort_key(network):
    return network.version, int(network.network_address), network.prefixlen


def resolve_prefix_groups(automatic_groups, overrides, priority=GROUP_PRIORITY):
    unknown = set(overrides) - set(automatic_groups)
    if unknown:
        raise ValueError(f"override contains unknown groups: {sorted(unknown)}")

    groups = {
        group: {ipaddress.ip_network(value, strict=False) for value in collapse(values)}
        for group, values in automatic_groups.items()
    }

    ordered_groups = [group for group in priority if group in groups]
    ordered_groups.extend(sorted(set(groups) - set(ordered_groups)))
    owner_by_prefix = {}
    for group in ordered_groups:
        for network in groups[group]:
            owner_by_prefix.setdefault(network, group)
    for group in groups:
        groups[group] = {
            network for network in groups[group] if owner_by_prefix[network] == group
        }

    manual_groups = {
        group: {
            ipaddress.ip_network(value, strict=False)
            for value in overrides.get(group, [])
        }
        for group in groups
    }
    claimed = []
    for group, networks in manual_groups.items():
        for network in sorted(networks, key=network_sort_key):
            for previous_group, previous_network in claimed:
                if previous_group != group and network.overlaps(previous_network):
                    raise ValueError(
                        f"manual prefixes {network} ({group}) and "
                        f"{previous_network} ({previous_group}) overlap"
                    )
            claimed.append((group, network))

    # A maintained CIDR owns its complete range. Remove automatic sub-prefixes
    # from every group before adding the maintained rule.
    for group, networks in manual_groups.items():
        for network in sorted(networks, key=network_sort_key):
            for candidate_group in groups:
                groups[candidate_group] = {
                    candidate
                    for candidate in groups[candidate_group]
                    if not candidate.subnet_of(network)
                }
            groups[group].add(network)

    return {
        group: [str(network) for network in sorted(networks, key=network_sort_key)]
        for group, networks in groups.items()
    }


def main():
    base = json.loads(BASE_RULES.read_text(encoding="utf-8"))
    overrides = json.loads(OVERRIDES.read_text(encoding="utf-8"))
    groups = {name: set() for name in base["asn_groups"]}
    asn_to_group = {}
    for group, asns in base["asn_groups"].items():
        for asn in asns:
            if asn in asn_to_group:
                raise ValueError(f"ASN {asn} is assigned to multiple groups")
            asn_to_group[asn] = group

    request = urllib.request.Request(TABLE_URL, headers={"User-Agent": USER_AGENT})
    with urllib.request.urlopen(request, timeout=120) as response:
        for raw_line in response:
            try:
                row = json.loads(raw_line)
                asn = int(str(row.get("ASN", "")).removeprefix("AS"))
                cidr = row.get("CIDR")
                group = asn_to_group.get(asn)
                if group and cidr:
                    groups[group].add(str(ipaddress.ip_network(cidr, strict=False)))
            except (json.JSONDecodeError, TypeError, ValueError):
                continue

    output = {
        "schema_version": 1,
        "generated_at": datetime.datetime.now(datetime.timezone.utc).isoformat().replace("+00:00", "Z"),
        "source": "bgp.tools/table.jsonl + maintained overrides",
        "prefix_groups": resolve_prefix_groups(groups, overrides),
    }
    OUTPUT.write_text(json.dumps(output, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


if __name__ == "__main__":
    main()
