import unittest

from tools.update_return_route_bgp_rules import resolve_prefix_groups


class ResolvePrefixGroupsTest(unittest.TestCase):
    def test_automatic_conflicts_follow_runtime_priority(self):
        groups = {
            "cn2_global": {"183.91.63.0/24"},
            "cn2_backbone": {"183.91.63.0/24"},
            "unicom_10099": {"203.0.113.0/24"},
            "unicom_9929": {"203.0.113.0/24"},
        }

        resolved = resolve_prefix_groups(groups, {})

        self.assertEqual(resolved["cn2_global"], ["183.91.63.0/24"])
        self.assertEqual(resolved["cn2_backbone"], [])
        self.assertEqual(resolved["unicom_10099"], ["203.0.113.0/24"])
        self.assertEqual(resolved["unicom_9929"], [])

    def test_manual_rule_replaces_automatic_subprefixes(self):
        groups = {
            "unicom_9929": set(),
            "unicom_4837": {"210.14.1.0/24", "210.13.0.0/16"},
        }

        resolved = resolve_prefix_groups(
            groups,
            {"unicom_9929": ["210.14.0.0/16", "210.14.160.0/19"]},
        )

        self.assertEqual(
            resolved["unicom_9929"],
            ["210.14.0.0/16", "210.14.160.0/19"],
        )
        self.assertEqual(resolved["unicom_4837"], ["210.13.0.0/16"])

    def test_overlapping_manual_rules_in_different_groups_are_rejected(self):
        groups = {"unicom_10099": set(), "unicom_9929": set()}

        with self.assertRaisesRegex(ValueError, "overlap"):
            resolve_prefix_groups(
                groups,
                {
                    "unicom_10099": ["203.0.113.0/24"],
                    "unicom_9929": ["203.0.113.128/25"],
                },
            )


if __name__ == "__main__":
    unittest.main()
