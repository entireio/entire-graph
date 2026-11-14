"""The disclosed 24-case synthetic pilot; expectations live in requirements."""
from itertools import product
from .contracts import Requirement, Scenario


def matrix() -> list[Scenario]:
    return [Scenario(scenario_id=f"{role}-{op}-{'own' if own else 'other'}-{visibility}",
                     role=role, operation=op, same_workspace=own, visibility=visibility)
            for role, op, own, visibility in product(
                ("guest", "member", "admin"), ("preview", "export"), (True, False), ("public", "private"))]


def matches(requirement: Requirement, scenario: Scenario) -> bool:
    request = scenario.request()
    return all(request[k] == v for k, v in requirement.scenario_filter.items())


def proposed_requirements(include_feature: bool = True) -> list[Requirement]:
    export = "pact/demo/workspace_app/export.py:export_document"
    preview = "pact/demo/workspace_app/preview.py:preview_document"
    rows = [
        ("R1", "Any guest export is denied, regardless of workspace or visibility.", False,
         {"role": "guest", "operation": "export"}, [export], ["base", "head"]),
        ("R2", "Members cannot access another workspace's private resources.", False,
         {"role": "member", "same_workspace": False, "visibility": "private"}, [preview, export], ["base", "head"]),
        ("R3", "Admins can export resources in their own workspace.", True,
         {"role": "admin", "operation": "export", "same_workspace": True}, [export], ["base", "head"]),
    ]
    if include_feature:
        rows.append(("R4", "Guests can preview public content in either workspace.", True,
                     {"role": "guest", "operation": "preview", "visibility": "public"}, [preview], ["head"]))
    return [Requirement(requirement_id=k, text=t, expected_allowed=e, scenario_filter=f,
                        entrypoints=ep, applies_to=sides) for k, t, e, f, ep, sides in rows]
