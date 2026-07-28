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


def collapse(values):
    networks = {ipaddress.ip_network(value, strict=False) for value in values}
    ipv4 = ipaddress.collapse_addresses(network for network in networks if network.version == 4)
    ipv6 = ipaddress.collapse_addresses(network for network in networks if network.version == 6)
    return [str(network) for network in [*ipv4, *ipv6]]


def main():
    base = json.loads(BASE_RULES.read_text(encoding="utf-8"))
    overrides = json.loads(OVERRIDES.read_text(encoding="utf-8"))
    groups = {name: set(overrides.get(name, [])) for name in base["asn_groups"]}
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
        "prefix_groups": {group: collapse(groups[group]) for group in base["asn_groups"]},
    }
    OUTPUT.write_text(json.dumps(output, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


if __name__ == "__main__":
    main()
