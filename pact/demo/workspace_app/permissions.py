def can_access(request):
    """H3: equivalent refactor; a guest can only preview public content."""
    guest = request["role"] == "guest"
    public_preview = request["visibility"] == "public" and request["operation"] == "preview"
    if guest:
        return public_preview
    return request["same_workspace"]
