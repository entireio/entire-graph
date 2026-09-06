from workspace_app.permissions import can_access


def export_document(request):
    return {"allowed": can_access(request)}
