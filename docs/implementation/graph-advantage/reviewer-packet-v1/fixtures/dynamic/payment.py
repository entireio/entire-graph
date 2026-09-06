def choose_handler(handlers, name):
    """Select payment handler dynamically."""
    return handlers[name]()
