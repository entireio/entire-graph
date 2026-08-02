const Config = struct { retries: u8 };

const Session = struct { config: Config };

fn connect(config: Config) Session {
    return open(config);
}

fn open(config: Config) Session {
    return Session{ .config = config };
}
