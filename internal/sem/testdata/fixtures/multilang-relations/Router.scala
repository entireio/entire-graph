class Route(val path: String)

object Router {
  def resolve(route: Route): Route = dispatch(route)

  def dispatch(route: Route): Route = route
}
