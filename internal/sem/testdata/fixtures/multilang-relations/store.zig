const Config = struct { retries: u8 };

fn connect(config: Config) Session {
    return open(config);
}

fn open(config: Config) Session {
    return Session{ .config = config };
}
