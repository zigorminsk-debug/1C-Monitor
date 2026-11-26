using System.Collections;
using System.Collections.Specialized;
using System.Globalization;
using Avalonia;
using Avalonia.Controls;
using Avalonia.Media;

namespace OneCMonitor.Controls;

public sealed record MetricChartPoint(
    DateTime At,
    double CpuPercent,
    double DiskPercent,
    double NetworkPercent,
    double MemoryGb,
    int ActiveSessions);

public sealed class MetricsChartControl : Control
{
    public static readonly StyledProperty<IEnumerable?> ItemsSourceProperty =
        AvaloniaProperty.Register<MetricsChartControl, IEnumerable?>(nameof(ItemsSource));

    public IEnumerable? ItemsSource
    {
        get => GetValue(ItemsSourceProperty);
        set => SetValue(ItemsSourceProperty, value);
    }

    static MetricsChartControl()
    {
        AffectsRender<MetricsChartControl>(ItemsSourceProperty);
    }

    protected override void OnPropertyChanged(AvaloniaPropertyChangedEventArgs change)
    {
        base.OnPropertyChanged(change);
        if (change.Property == ItemsSourceProperty)
        {
            if (change.OldValue is INotifyCollectionChanged oldCol)
                oldCol.CollectionChanged -= OnCollectionChanged;
            if (change.NewValue is INotifyCollectionChanged newCol)
                newCol.CollectionChanged += OnCollectionChanged;
            InvalidateVisual();
        }
    }

    private void OnCollectionChanged(object? sender, NotifyCollectionChangedEventArgs e) => InvalidateVisual();

    public override void Render(DrawingContext context)
    {
        base.Render(context);
        var bounds = Bounds;
        if (bounds.Width < 80 || bounds.Height < 60) return;

        var bgBrush = new SolidColorBrush(Color.Parse("#0B1320"));
        var borderPen = new Pen(new SolidColorBrush(Color.Parse("#273449")), 1);
        context.DrawRectangle(bgBrush, borderPen, new Rect(0, 0, bounds.Width, bounds.Height), 6, 6);

        var points = (ItemsSource?.OfType<MetricChartPoint>().ToList()) ?? new List<MetricChartPoint>();
        var typeface = new Typeface("Segoe UI");

        if (points.Count < 2)
        {
            var hint = new FormattedText(
                "Накопление точек графика (появится после 2-го замера)…",
                CultureInfo.CurrentCulture,
                FlowDirection.LeftToRight,
                typeface,
                12,
                new SolidColorBrush(Color.Parse("#94A3B8")));
            context.DrawText(hint, new Point(14, bounds.Height / 2 - 8));
            return;
        }

        const double padLeft = 42;
        const double padRight = 14;
        const double padTop = 26;
        const double padBottom = 24;
        var plotW = Math.Max(20, bounds.Width - padLeft - padRight);
        var plotH = Math.Max(20, bounds.Height - padTop - padBottom);

        var gridPen = new Pen(new SolidColorBrush(Color.Parse("#1E293B")), 1);
        var labelBrush = new SolidColorBrush(Color.Parse("#94A3B8"));

        for (var i = 0; i <= 4; i++)
        {
            var frac = i / 4d;
            var y = padTop + plotH * (1 - frac);
            context.DrawLine(gridPen, new Point(padLeft, y), new Point(padLeft + plotW, y));
            var pctLabel = new FormattedText($"{frac * 100:0}%", CultureInfo.CurrentCulture, FlowDirection.LeftToRight, typeface, 10, labelBrush);
            context.DrawText(pctLabel, new Point(6, y - 6));
        }

        var maxSessions = Math.Max(10, points.Max(p => p.ActiveSessions));
        DrawSeries(points.Select(p => Math.Clamp(p.CpuPercent, 0, 100)), Color.Parse("#38BDF8"), 2.0);
        DrawSeries(points.Select(p => Math.Clamp(p.DiskPercent, 0, 100)), Color.Parse("#FACC15"), 1.8);
        DrawSeries(points.Select(p => Math.Clamp(p.NetworkPercent, 0, 100)), Color.Parse("#34D399"), 1.6);
        DrawSeries(points.Select(p => Math.Clamp(p.ActiveSessions * 100d / maxSessions, 0, 100)), Color.Parse("#C084FC"), 1.6);

        var last = points[^1];
        var legend = new FormattedText(
            $"● CPU rphost: {last.CpuPercent:0.0}%   ● Диск: {last.DiskPercent:0.0}%   ● Сеть: {last.NetworkPercent:0.0}%   ● Акт. сеансы: {last.ActiveSessions} (шкала до {maxSessions})   |   RAM: {last.MemoryGb:0.00} ГБ",
            CultureInfo.CurrentCulture,
            FlowDirection.LeftToRight,
            typeface,
            11,
            new SolidColorBrush(Color.Parse("#E2E8F0")));
        context.DrawText(legend, new Point(padLeft, 6));

        var startTime = new FormattedText(points[0].At.ToString("HH:mm:ss"), CultureInfo.CurrentCulture, FlowDirection.LeftToRight, typeface, 10, labelBrush);
        var endTime = new FormattedText(last.At.ToString("HH:mm:ss"), CultureInfo.CurrentCulture, FlowDirection.LeftToRight, typeface, 10, labelBrush);
        context.DrawText(startTime, new Point(padLeft, padTop + plotH + 4));
        context.DrawText(endTime, new Point(Math.Max(padLeft + 60, padLeft + plotW - endTime.Width), padTop + plotH + 4));

        void DrawSeries(IEnumerable<double> values, Color color, double thickness)
        {
            var list = values.ToList();
            if (list.Count < 2) return;
            var pen = new Pen(new SolidColorBrush(color), thickness);
            var geom = new StreamGeometry();
            using (var ctx = geom.Open())
            {
                for (var i = 0; i < list.Count; i++)
                {
                    var x = padLeft + plotW * i / (list.Count - 1);
                    var y = padTop + plotH * (1 - list[i] / 100d);
                    var pt = new Point(x, y);
                    if (i == 0) ctx.BeginFigure(pt, false);
                    else ctx.LineTo(pt);
                }
                ctx.EndFigure(false);
            }
            context.DrawGeometry(null, pen, geom);
        }
    }
}
