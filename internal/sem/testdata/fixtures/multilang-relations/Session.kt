class Session(val token: String)

fun renew(session: Session): Session {
    return refresh(session)
}

fun refresh(session: Session): Session {
    return session
}
