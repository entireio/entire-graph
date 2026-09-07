from workspace_app import permissions


def export_document(request):
    """Team-authored Curveball fixture: the permission target is looked up at runtime."""
    check = getattr(permissions, "can_" + "access")
    return {"allowed": check(request)}
