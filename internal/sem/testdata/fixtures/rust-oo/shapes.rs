pub trait Shape {
    fn area(&self) -> f64;
}

pub trait Drawable: Shape {
    fn draw(&self);
}

pub struct Circle {
    pub radius: f64,
}

impl Shape for Circle {
    fn area(&self) -> f64 {
        3.14 * self.radius * self.radius
    }
}

impl Drawable for Circle {
    fn draw(&self) {}
}

impl Circle {
    // A `fn` declared inside a method body is an ITEM scoped to that BLOCK, not
    // an associated function of Circle (issue #259). It takes no `self` and
    // `Circle::area` resolves only to the trait impl above, so this must emit
    // as the function `Circle.scaled.area` — emitting the method `Circle.area`
    // would name a second, non-existent associated function and push the real
    // one onto a `#sig:`-disambiguated ID.
    pub fn scaled(&self, factor: f64) -> f64 {
        fn area(radius: f64) -> f64 {
            3.14 * radius * radius
        }

        area(self.radius) * factor
    }

    // A same-named nested item in a DIFFERENT method must stay distinct rather
    // than collapsing onto one ID.
    pub fn halved(&self) -> f64 {
        fn area(radius: f64) -> f64 {
            1.57 * radius * radius
        }

        area(self.radius)
    }
}
