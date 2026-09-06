class Report {
    String title

    String render(Report other) {
        return format(other)
    }

    String format(Report other) {
        return other.title
    }
}
