def can_access(request):
    """B0 policy: guests denied; authenticated roles stay in their workspace."""
    if request["role"] == "guest":
        return False
    return request["same_workspace"]
