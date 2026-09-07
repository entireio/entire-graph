from workspace_app.preview import preview_document
from workspace_app.export import export_document


def dispatch(request):
    if request["operation"] == "preview":
        return preview_document(request)
    if request["operation"] == "export":
        return export_document(request)
    raise ValueError("Unsupported operation")
