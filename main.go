package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// bufferDrainInterval - интервал очистки кольцевого буфера
const bufferDrainInterval time.Duration = 15 * time.Second

// bufferSize - размер кольцевого буфера
const bufferSize int = 10

// RingBuffer - кольцевой буфер целых чисел
type RingBuffer struct {
	data     []int
	head     int
	tail     int
	size     int // количество элементов в буфере
	capacity int // максимальная емкость
	m        sync.Mutex
}

// NewRingBuffer - создание нового кольцевого буфера с заданной емкостью
func NewRingBuffer(capacity int) *RingBuffer {
	return &RingBuffer{
		data:     make([]int, capacity),
		head:     0,
		tail:     0,
		size:     0,
		capacity: capacity,
		m:        sync.Mutex{},
	}
}

// Push - добавление элемента в конец буфера
func (r *RingBuffer) Push(value int) {
	r.m.Lock()
	defer r.m.Unlock()

	r.data[r.tail] = value
	r.tail = (r.tail + 1) % r.capacity

	if r.size == r.capacity {
		r.head = (r.head + 1) % r.capacity
	} else {
		r.size++
	}
}

// Get - получение данных с последующей очисткой
func (r *RingBuffer) Get() []int {
	r.m.Lock()
	defer r.m.Unlock()

	if r.size == 0 {
		return []int{}
	}

	output := make([]int, r.size)
	if r.head < r.tail {
		copy(output, r.data[r.head:r.tail])
	} else {
		copy(output, r.data[r.head:])
		copy(output[r.capacity-r.head:], r.data[:r.tail])
	}

	// очистка буфера
	for i := 0; i < r.capacity; i++ {
		r.data[i] = 0
	}
	r.head = 0
	r.tail = 0
	r.size = 0

	return output
}

// Stage - стадия пайплайна, обрабатывающая целые числа
type Stage func(context.Context, <-chan int) <-chan int

// Pipeline - пайплайн обработки целых чисел
type Pipeline struct {
	ctx    context.Context
	stages []Stage
}

// NewPipeline - инициализация пайплайна
func NewPipeline(ctx context.Context, stages ...Stage) *Pipeline {
	return &Pipeline{ctx: ctx, stages: stages}
}

// RunStage - запуск отдельной стадии пайплайна
func (p *Pipeline) RunStage(inputChan <-chan int, stage Stage) <-chan int {
	return stage(p.ctx, inputChan)
}

// Run - запуск пайплайна
func (p *Pipeline) Run() <-chan int {
	if len(p.stages) == 0 {
		outputChan := make(chan int)
		close(outputChan)
		return outputChan
	}

	var outputChan <-chan int = p.stages[0](p.ctx, nil)

	for i := 1; i < len(p.stages); i++ {
		outputChan = p.RunStage(outputChan, p.stages[i])
	}
	return outputChan
}

// SourceStage - стадия источника данных (чтение из консоли)
func SourceStage(ctx context.Context, _ <-chan int) <-chan int {
	outputChan := make(chan int)

	go func() {
		defer close(outputChan)
		// создаем новый сканер, который будет построчно
		// считывать ввод пользователя с консоли
		scanner := bufio.NewScanner(os.Stdin)
		for {
			select {
			case <-ctx.Done():
				return
			default:
				fmt.Println("Введите число (или 'exit' для выхода):")
				if !scanner.Scan() {
					if err := scanner.Err(); err != nil {
						fmt.Fprintf(os.Stderr, "Ошибка при чтении ввода: %v\n", err)
					}
					return
				}
				data := scanner.Text()

				// проверка на команду выхода
				if strings.EqualFold(data, "exit") {
					fmt.Println("Программа завершает работу!")
					return
				}

				// преобразование строки в число
				i, err := strconv.Atoi(data)
				if err != nil {
					fmt.Println("Программа обрабатывает только целые числа!")
					continue
				}

				// отправка данных в канал или обработка сигнала завершения
				select {
				case outputChan <- i:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return outputChan
}

// NegativeFilterStage - стадия фильтрации отрицательных чисел
func NegativeFilterStage(ctx context.Context, intputChan <-chan int) <-chan int {
	outputChan := make(chan int)

	go func() {
		defer close(outputChan)
		for {
			select {
			case data, ok := <-intputChan:
				if !ok {
					fmt.Println("Канал закрыт, завершаем работу NegativeFilterStage.")
					return
				}
				if data >= 0 {
					select {
					case outputChan <- data:
					case <-ctx.Done():
						return
					}
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return outputChan
}

// SpecialFilterStage - стадия фильтрации чисел, не кратных трем
func SpecialFilterStage(ctx context.Context, inputChan <-chan int) <-chan int {
	outputChan := make(chan int)

	go func() {
		defer close(outputChan)
		for {
			select {
			case data, ok := <-inputChan:
				if !ok {
					fmt.Println("Канал закрыт, завершаем работу SpecialFilterStage.")
					return
				}
				if data != 0 && data%3 == 0 {
					select {
					case outputChan <- data:
					case <-ctx.Done():
						return
					}
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return outputChan
}

// BufferStage - стадия буферизации
func BufferStage(ctx context.Context, inputChan <-chan int) <-chan int {
	outputChan := make(chan int)
	buffer := NewRingBuffer(bufferSize)

	go func() {
		defer close(outputChan)
		for {
			select {
			case data, ok := <-inputChan:
				if !ok {
					fmt.Println("Канал закрыт, завершаем работу BufferStage.")
					bufferData := buffer.Get()
					for _, val := range bufferData {
						select {
						case outputChan <- val:
						case <-ctx.Done():
							return
						}
					}
					return
				}
				buffer.Push(data)
			case <-time.After(bufferDrainInterval):
				bufferData := buffer.Get()
				if len(bufferData) > 0 {
					for _, data := range bufferData {
						select {
						case outputChan <- data:
						case <-ctx.Done():
							return
						}
					}
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return outputChan
}

// ConsumerStage - стадия потребителя данных (вывод в консоль)
func ConsumerStage(ctx context.Context, inputChan <-chan int) <-chan int {
	outputChan := make(chan int)

	go func() {
		defer close(outputChan)
		for {
			select {
			case data, ok := <-inputChan:
				if !ok {
					fmt.Println("Канал закрыт, завершаем работу ConsumerStage.")
					return
				}
				fmt.Printf("Получены данные: %d\n", data)
			case <-ctx.Done():
				return
			}
		}
	}()
	return outputChan
}

func main() {
	// создаем контекст с возможностью отмены
	ctx, canсel := context.WithCancel(context.Background())
	defer canсel()

	// Создаем пайплайн
	pipeline := NewPipeline(
		ctx,
		SourceStage,
		NegativeFilterStage,
		SpecialFilterStage,
		BufferStage,
		ConsumerStage,
	)

	// Запускаем пайплайн
	outputChan := pipeline.Run()

	// Читаем из конечного канала, чтобы пайплайн работал
	for range outputChan {
	}
}
