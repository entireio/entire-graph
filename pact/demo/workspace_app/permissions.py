def can_access(request):
    """H1: deliberately seeded overly broad guest-public exception."""
    if request["role"] == "guest" and request["visibility"] == "public":
        return True
    if request["role"] == "guest":
        return False
    return request["same_workspace"]
