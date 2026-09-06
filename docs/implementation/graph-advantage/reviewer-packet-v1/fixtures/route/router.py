from handler import receive_event

def dispatch_webhook(event):
    """Dispatch incoming webhook event."""
    return receive_event(event)
