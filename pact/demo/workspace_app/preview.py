from workspace_app.permissions import can_access


def preview_document(request):
    return {"allowed": can_access(request)}
