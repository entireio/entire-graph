namespace Edge
{
    public partial class Widget
    {
        public void Grow() { Size++; }
    }

    public static class WidgetExtensions
    {
        public static bool IsEmpty(this Widget widget) { return widget.Size == 0; }
    }
}
