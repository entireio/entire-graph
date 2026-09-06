def can_access(request):
    """H4: seeded alternative policy; requires explicit revised approval."""
    guest = request["role"] == "guest"
    if guest:
        return request["visibility"] == "public"
    return request["same_workspace"]
