from typing import overload


class Named:
    def name(self):
        return "thing"


class Loud:
    def shout(self):
        return "noise"


class Dog(Named, Loud):
    def shout(self):
        return "woof"

    # A callable declared in a method body is a local binding of that method,
    # not a member of Dog (issue #199). It must emit as the function
    # `Dog.describe.amplify`, NOT as the method `Dog.amplify` — which would be
    # a member no Dog has, and which would collide with the real `Dog.name`
    # inherited surface and with the module-level `describe` below.
    def describe(self):
        def amplify(sound):
            return sound.upper()

        return amplify(self.shout())

    # A same-named nested callable in a DIFFERENT method must stay distinct
    # rather than collapsing onto one ID.
    def whisper(self):
        def amplify(sound):
            return sound.lower()

        return amplify(self.shout())


# @overload stubs are type-only and must NOT be emitted as symbols; only the
# implementation `describe` below should appear.
@overload
def describe(x: int) -> str: ...
@overload
def describe(x: str) -> str: ...
def describe(x):
    return str(x)
