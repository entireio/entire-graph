<?php

namespace Shapes;

interface Shape
{
    public function area(): float;
}

abstract class Base
{
    abstract public function describe(): string;
}

class Circle extends Base implements Shape
{
    public function area(): float
    {
        return 3.14;
    }

    public function describe(): string
    {
        return "circle";
    }

    // PHP has no nested function scope: running scaled() declares `area` in the
    // CURRENT NAMESPACE, after which it is the plain function `Shapes\area()`.
    // `Circle::area` is the real member above and calling it as a method is a
    // fatal error, so this must emit as the function `Circle.scaled.area` —
    // emitting the method `Circle.area` invents a second member and pushes the
    // real one onto a `#sig:`-disambiguated ID (issue #259).
    public function scaled(float $factor): float
    {
        function area(float $radius): float
        {
            return 3.14 * $radius;
        }

        return area($factor);
    }
}
