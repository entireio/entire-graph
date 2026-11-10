class Session(val token: String) {
    // A `fun` declared inside a function body is a LOCAL FUNCTION, scoped to
    // rotate() and invisible outside it, so it must emit as the function
    // `Session.rotate.derive` — emitting the method `Session.derive` invents a
    // member and pushes the real `derive` below onto a `#sig:`-disambiguated
    // ID (issue #259).
    fun rotate(): String {
        fun derive(seed: String): String {
            return seed
        }

        return derive(token)
    }

    fun derive(): String {
        return token
    }
}

fun renew(session: Session): Session {
    return refresh(session)
}

fun refresh(session: Session): Session {
    return session
}
