def can_access(request):
    """H2: public preview is allowed without allowing guest exports."""
    if (request["role"] == "guest" and request["visibility"] == "public"
            and request["operation"] == "preview"):
        return True
    if request["role"] == "guest":
        return False
    return request["same_workspace"]
