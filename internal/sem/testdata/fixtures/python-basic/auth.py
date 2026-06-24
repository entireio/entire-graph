import json


class AuthService:
    def validate(self, token):
        return bool(token)

    def describe(self):
        return json.dumps({"service": "auth"})


def check_token(token):
    service = AuthService()
    return service.validate(token)
